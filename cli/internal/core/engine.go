package core

import (
	"fmt"
	"strings"

	"github.com/vercel/turbo/cli/internal/util"

	"github.com/pyr-sh/dag"
)

const ROOT_NODE_NAME = "___ROOT___"

type Task struct {
	Name string
	// Deps are dependencies between tasks within the same package (e.g. `build` -> `test`)
	Deps util.Set
	// TopoDeps are dependencies across packages within the same topological graph (e.g. parent `build` -> child `build`) */
	TopoDeps util.Set
	// Persistent is whether this task is persistent or not. We need this information to validate TopoDeps graph
	Persistent bool
}

type Visitor = func(taskID string) error

// Engine contains both the DAG for the packages and the tasks and implements the methods to execute tasks in them
type Engine struct {
	// TopologicGraph is a graph of workspaces
	TopologicGraph *dag.AcyclicGraph
	// TaskGraph is a graph of package-tasks
	TaskGraph *dag.AcyclicGraph
	// Tasks are a map of tasks in the engine
	Tasks            map[string]*Task
	PackageTaskDeps  [][]string
	rootEnabledTasks util.Set
}

// NewEngine creates a new engine given a topologic graph of workspace package names
func NewEngine(topologicalGraph *dag.AcyclicGraph) *Engine {
	return &Engine{
		Tasks:            make(map[string]*Task),
		TopologicGraph:   topologicalGraph,
		TaskGraph:        &dag.AcyclicGraph{},
		PackageTaskDeps:  [][]string{},
		rootEnabledTasks: make(util.Set),
	}
}

// EngineExecutionOptions are options for a single engine execution
type EngineExecutionOptions struct {
	// Packages in the execution scope, if nil, all packages will be considered in scope
	Packages []string
	// TaskNames in the execution scope, if nil, all tasks will be executed
	TaskNames []string
	// Restrict execution to only the listed task names
	TasksOnly bool
}

// Prepare constructs the Task Graph for a list of packages and tasks
func (e *Engine) Prepare(options *EngineExecutionOptions) error {
	pkgs := options.Packages
	tasks := options.TaskNames
	if len(tasks) == 0 {
		// TODO(gsoltis): Is this behavior used?
		for key := range e.Tasks {
			tasks = append(tasks, key)
		}
	}

	if err := e.generateTaskGraph(pkgs, tasks, options.TasksOnly); err != nil {

		return err
	}

	return nil
}

// ExecOpts controls a single walk of the task graph
type ExecOpts struct {
	// Parallel is whether to run tasks in parallel
	Parallel bool
	// Concurrency is the number of concurrent tasks that can be executed
	Concurrency int
}

// Execute executes the pipeline, constructing an internal task graph and walking it accordingly.
func (e *Engine) Execute(visitor Visitor, opts ExecOpts) []error {
	var sema = util.NewSemaphore(opts.Concurrency)
	return e.TaskGraph.Walk(func(v dag.Vertex) error {
		// Always return if it is the root node
		if strings.Contains(dag.VertexName(v), ROOT_NODE_NAME) {
			return nil
		}
		// Acquire the semaphore unless parallel
		if !opts.Parallel {
			sema.Acquire()
			defer sema.Release()
		}
		return visitor(dag.VertexName(v))
	})
}

func (e *Engine) getTaskDefinition(pkg string, taskName string, taskID string) (*Task, error) {
	if task, ok := e.Tasks[taskID]; ok {
		return task, nil
	}
	if task, ok := e.Tasks[taskName]; ok {
		return task, nil
	}

	return nil, fmt.Errorf("Missing task definition, configure \"%s\" or \"%s\" in turbo.json", taskName, taskID)
}

func (e *Engine) generateTaskGraph(pkgs []string, taskNames []string, tasksOnly bool) error {
	if e.PackageTaskDeps == nil {
		e.PackageTaskDeps = [][]string{}
	}

	packageTasksDepsMap := getPackageTaskDepsMap(e.PackageTaskDeps)

	traversalQueue := []string{}
	for _, pkg := range pkgs {
		isRootPkg := pkg == util.RootPkgName
		for _, taskName := range taskNames {
			if !isRootPkg || e.rootEnabledTasks.Includes(taskName) {
				taskID := util.GetTaskId(pkg, taskName)
				if _, err := e.getTaskDefinition(pkg, taskName, taskID); err != nil {
					// Initial, non-package tasks are not required to exist, as long as some
					// package in the list packages defines it as a package-task. Dependencies
					// *are* required to have a definition.
					continue
				}
				traversalQueue = append(traversalQueue, taskID)
			}
		}
	}

	visited := make(util.Set)

	// Things get appended to traversalQueue inside this loop, so we use the len() check instead of range.
	for len(traversalQueue) > 0 {
		// pop off the first item from the traversalQueue
		taskID := traversalQueue[0]
		traversalQueue = traversalQueue[1:]

		pkg, taskName := util.GetPackageTaskFromId(taskID)
		if pkg == util.RootPkgName && !e.rootEnabledTasks.Includes(taskName) {
			return fmt.Errorf("%v needs an entry in turbo.json before it can be depended on because it is a task run from the root package", taskID)
		}

		task, err := e.getTaskDefinition(pkg, taskName, taskID)

		if err != nil {
			return err
		}

		// Exit loop if we've already seen this taskID.
		if visited.Includes(taskID) {
			continue
		}

		visited.Add(taskID)

		deps := task.Deps

		if tasksOnly {
			deps = deps.Filter(func(d interface{}) bool {
				for _, target := range taskNames {
					return fmt.Sprintf("%v", d) == target
				}
				return false
			})
			task.TopoDeps = task.TopoDeps.Filter(func(d interface{}) bool {
				for _, target := range taskNames {
					return fmt.Sprintf("%v", d) == target
				}
				return false
			})
		}

		hasTopoDeps := task.TopoDeps.Len() > 0 && e.TopologicGraph.DownEdges(pkg).Len() > 0
		hasDeps := deps.Len() > 0

		hasPackageTaskDeps := false
		if _, ok := packageTasksDepsMap[taskID]; ok {
			hasPackageTaskDeps = true
		}

		if hasTopoDeps {
			depPkgs := e.TopologicGraph.DownEdges(pkg)
			for _, from := range task.TopoDeps.UnsafeListOfStrings() {
				// add task dep from all the package deps within repo
				for depPkg := range depPkgs {
					fromTaskID, err := e.addTaskToGraph(taskID, from, depPkg.(string))
					if err != nil {
						return err
					}
					traversalQueue = append(traversalQueue, fromTaskID)

				}
			}
		}

		if hasDeps {
			for _, from := range deps.UnsafeListOfStrings() {
				fromTaskID, err := e.addTaskToGraph(taskID, from, pkg)
				if err != nil {
					return err
				}
				traversalQueue = append(traversalQueue, fromTaskID)
			}
		}

		if hasPackageTaskDeps {
			if pkgTaskDeps, ok := packageTasksDepsMap[taskID]; ok {
				for _, fromTaskID := range pkgTaskDeps {
					// TODO: Is this right?
					fromTaskID, err := e.addTaskToGraph(taskID, fromTaskID, "")
					if err != nil {
						return err
					}
					traversalQueue = append(traversalQueue, fromTaskID)
				}
			}
		}

		if !hasDeps && !hasTopoDeps && !hasPackageTaskDeps {
			e.TaskGraph.Add(ROOT_NODE_NAME)
			e.TaskGraph.Add(taskID)
			e.TaskGraph.Connect(dag.BasicEdge(taskID, ROOT_NODE_NAME))
		}

	}

	return nil
}

func (e *Engine) addTaskToGraph(taskID string, from string, pkgName string) (string, error) {
	fromTaskID := util.GetTaskId(pkgName, from)
	fromTask, err := e.getTaskDefinition(pkgName, from, fromTaskID)

	if err != nil {
		return "", fmt.Errorf("Could not find taskID \"%s\" in graph. This is likely a bug, please file an issue at https://github.com/vercel/turbo/issues/new", taskID)
	}

	if fromTask.Persistent {
		return "", fmt.Errorf("Persistent tasks cannot depend on other persistent tasks. Found %#v depends on %#v", taskID, fromTaskID)
	}

	e.TaskGraph.Add(fromTaskID)
	e.TaskGraph.Add(taskID)
	e.TaskGraph.Connect(dag.BasicEdge(taskID, fromTaskID))
	return fromTaskID, nil
}

func getPackageTaskDepsMap(packageTaskDeps [][]string) map[string][]string {
	depMap := make(map[string][]string)
	for _, packageTaskDep := range packageTaskDeps {
		from := packageTaskDep[0]
		to := packageTaskDep[1]
		if _, ok := depMap[to]; !ok {
			depMap[to] = []string{}
		}
		depMap[to] = append(depMap[to], from)
	}
	return depMap
}

// AddTask adds a task to the Engine so it can be looked up later.
func (e *Engine) AddTask(task *Task) *Engine {
	// If a root task is added, mark the task name as eligible for
	// root execution. Otherwise, it will be skipped.
	if util.IsPackageTask(task.Name) {
		pkg, taskName := util.GetPackageTaskFromId(task.Name)
		if pkg == util.RootPkgName {
			e.rootEnabledTasks.Add(taskName)
		}
	}
	e.Tasks[task.Name] = task
	return e
}

// AddDep adds tuples from+to task ID combos in tuple format so they can be looked up later.
func (e *Engine) AddDep(fromTaskID string, toTaskID string) error {
	fromPkg, _ := util.GetPackageTaskFromId(fromTaskID)
	if fromPkg != ROOT_NODE_NAME && fromPkg != util.RootPkgName && !e.TopologicGraph.HasVertex(fromPkg) {
		return fmt.Errorf("found reference to unknown package: %v in task %v", fromPkg, fromTaskID)
	}
	e.PackageTaskDeps = append(e.PackageTaskDeps, []string{fromTaskID, toTaskID})
	return nil
}
