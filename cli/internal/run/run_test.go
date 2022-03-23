package run

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mitchellh/cli"
	"github.com/vercel/turborepo/cli/internal/fs"

	"github.com/stretchr/testify/assert"
)

func TestParseConfig(t *testing.T) {
	defaultCwd, err := os.Getwd()
	if err != nil {
		t.Errorf("failed to get cwd: %v", err)
	}
	defaultCacheFolder := filepath.Join(defaultCwd, filepath.FromSlash("node_modules/.cache/turbo"))
	cases := []struct {
		Name     string
		Args     []string
		Expected *RunOptions
	}{
		{
			"string flags",
			[]string{"foo"},
			&RunOptions{
				includeDependents:   true,
				stream:              true,
				bail:                true,
				dotGraph:            "",
				concurrency:         10,
				includeDependencies: false,
				cache:               true,
				forceExecution:      false,
				profile:             "",
				cwd:                 defaultCwd,
				cacheFolder:         defaultCacheFolder,
				cacheHitLogsMode:    FullLogs,
				cacheMissLogsMode:   FullLogs,
			},
		},
		{
			"scope",
			[]string{"foo", "--scope=foo", "--scope=blah"},
			&RunOptions{
				includeDependents:   true,
				stream:              true,
				bail:                true,
				dotGraph:            "",
				concurrency:         10,
				includeDependencies: false,
				cache:               true,
				forceExecution:      false,
				profile:             "",
				scope:               []string{"foo", "blah"},
				cwd:                 defaultCwd,
				cacheFolder:         defaultCacheFolder,
				cacheHitLogsMode:    FullLogs,
				cacheMissLogsMode:   FullLogs,
			},
		},
		{
			"concurrency",
			[]string{"foo", "--concurrency=12"},
			&RunOptions{
				includeDependents:   true,
				stream:              true,
				bail:                true,
				dotGraph:            "",
				concurrency:         12,
				includeDependencies: false,
				cache:               true,
				forceExecution:      false,
				profile:             "",
				cwd:                 defaultCwd,
				cacheFolder:         defaultCacheFolder,
				cacheHitLogsMode:    FullLogs,
				cacheMissLogsMode:   FullLogs,
			},
		},
		{
			"graph",
			[]string{"foo", "--graph=g.png"},
			&RunOptions{
				includeDependents:   true,
				stream:              true,
				bail:                true,
				dotGraph:            "g.png",
				concurrency:         10,
				includeDependencies: false,
				cache:               true,
				forceExecution:      false,
				profile:             "",
				cwd:                 defaultCwd,
				cacheFolder:         defaultCacheFolder,
				cacheHitLogsMode:    FullLogs,
				cacheMissLogsMode:   FullLogs,
			},
		},
		{
			"passThroughArgs",
			[]string{"foo", "--graph=g.png", "--", "--boop", "zoop"},
			&RunOptions{
				includeDependents:   true,
				stream:              true,
				bail:                true,
				dotGraph:            "g.png",
				concurrency:         10,
				includeDependencies: false,
				cache:               true,
				forceExecution:      false,
				profile:             "",
				cwd:                 defaultCwd,
				cacheFolder:         defaultCacheFolder,
				passThroughArgs:     []string{"--boop", "zoop"},
				cacheHitLogsMode:    FullLogs,
				cacheMissLogsMode:   FullLogs,
			},
		},
		{
			"Empty passThroughArgs",
			[]string{"foo", "--graph=g.png", "--"},
			&RunOptions{
				includeDependents:   true,
				stream:              true,
				bail:                true,
				dotGraph:            "g.png",
				concurrency:         10,
				includeDependencies: false,
				cache:               true,
				forceExecution:      false,
				profile:             "",
				cwd:                 defaultCwd,
				cacheFolder:         defaultCacheFolder,
				passThroughArgs:     []string{},
				cacheHitLogsMode:    FullLogs,
				cacheMissLogsMode:   FullLogs,
			},
		},
		{
			"since and scope imply including dependencies for backwards compatibility",
			[]string{"foo", "--scope=bar", "--since=some-ref"},
			&RunOptions{
				includeDependents:   true,
				stream:              true,
				bail:                true,
				concurrency:         10,
				includeDependencies: true,
				cache:               true,
				cwd:                 defaultCwd,
				cacheFolder:         defaultCacheFolder,
				scope:               []string{"bar"},
				since:               "some-ref",
				cacheHitLogsMode:    FullLogs,
				cacheMissLogsMode:   FullLogs,
			},
		},
	}

	ui := &cli.BasicUi{
		Reader:      os.Stdin,
		Writer:      os.Stdout,
		ErrorWriter: os.Stderr,
	}

	for i, tc := range cases {
		t.Run(fmt.Sprintf("%d-%s", i, tc.Name), func(t *testing.T) {

			actual, err := parseRunArgs(tc.Args, defaultCwd, ui)
			if err != nil {
				t.Fatalf("invalid parse: %#v", err)
			}
			assert.EqualValues(t, tc.Expected, actual)
		})
	}
}

func TestParseRunOptionsUsesCWDFlag(t *testing.T) {
	expected := &RunOptions{
		includeDependents:   true,
		stream:              true,
		bail:                true,
		dotGraph:            "",
		concurrency:         10,
		includeDependencies: false,
		cache:               true,
		forceExecution:      false,
		profile:             "",
		cwd:                 "zop",
		cacheFolder:         filepath.FromSlash("zop/node_modules/.cache/turbo"),
		cacheHitLogsMode:    FullLogs,
		cacheMissLogsMode:   FullLogs,
	}

	ui := &cli.BasicUi{
		Reader:      os.Stdin,
		Writer:      os.Stdout,
		ErrorWriter: os.Stderr,
	}

	t.Run("accepts cwd argument", func(t *testing.T) {
		// Note that the Run parsing actually ignores `--cwd=` arg since
		// the `--cwd=` is parsed when setting up the global Config. This value is
		// passed directly as an argument to the parser.
		// We still need to ensure run accepts cwd flag and doesn't error.
		actual, err := parseRunArgs([]string{"foo", "--cwd=zop"}, "zop", ui)
		if err != nil {
			t.Fatalf("invalid parse: %#v", err)
		}
		assert.EqualValues(t, expected, actual)
	})

}

func TestGetTargetsFromArguments(t *testing.T) {
	type args struct {
		arguments  []string
		configJson *fs.TurboConfigJSON
	}
	tests := []struct {
		name    string
		args    args
		want    []string
		wantErr bool
	}{
		{
			name: "handles one defined target",
			args: args{
				arguments: []string{"build"},
				configJson: &fs.TurboConfigJSON{
					Pipeline: map[string]fs.Pipeline{
						"build":      {},
						"test":       {},
						"thing#test": {},
					},
				},
			},
			want:    []string{"build"},
			wantErr: false,
		},
		{
			name: "handles multiple targets and ignores flags",
			args: args{
				arguments: []string{"build", "test", "--foo", "--bar"},
				configJson: &fs.TurboConfigJSON{
					Pipeline: map[string]fs.Pipeline{
						"build":      {},
						"test":       {},
						"thing#test": {},
					},
				},
			},
			want:    []string{"build", "test"},
			wantErr: false,
		},
		{
			name: "handles pass through arguments after -- ",
			args: args{
				arguments: []string{"build", "test", "--", "--foo", "build", "--cache-dir"},
				configJson: &fs.TurboConfigJSON{
					Pipeline: map[string]fs.Pipeline{
						"build":      {},
						"test":       {},
						"thing#test": {},
					},
				},
			},
			want:    []string{"build", "test"},
			wantErr: false,
		},
		{
			name: "handles unknown pipeline targets ",
			args: args{
				arguments: []string{"foo", "test", "--", "--foo", "build", "--cache-dir"},
				configJson: &fs.TurboConfigJSON{
					Pipeline: map[string]fs.Pipeline{
						"build":      {},
						"test":       {},
						"thing#test": {},
					},
				},
			},
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getTargetsFromArguments(tt.args.arguments, tt.args.configJson)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetTargetsFromArguments() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetTargetsFromArguments() = %v, want %v", got, tt.want)
			}
		})
	}
}