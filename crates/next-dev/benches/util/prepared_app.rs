use std::{
    future::Future,
    path::{Path, PathBuf},
    pin::Pin,
    process::Child,
};

use anyhow::{anyhow, Context, Result};
use chromiumoxide::{
    cdp::{
        browser_protocol::network::EventResponseReceived,
        js_protocol::runtime::{AddBindingParams, EventBindingCalled, EventExceptionThrown},
    },
    Browser, Page,
};
use futures::{FutureExt, StreamExt};
use tokio::task::spawn_blocking;
use url::Url;

use crate::{bundlers::Bundler, util::PageGuard, BINDING_NAME};

fn copy_dir_boxed(
    from: PathBuf,
    to: PathBuf,
) -> Pin<Box<dyn Future<Output = anyhow::Result<()>> + Sync + Send>> {
    Box::pin(copy_dir(from, to))
}

async fn copy_dir(from: PathBuf, to: PathBuf) -> anyhow::Result<()> {
    let dir = spawn_blocking(|| std::fs::read_dir(from)).await??;
    let mut jobs = Vec::new();
    let mut file_futures = Vec::new();
    for entry in dir {
        let entry = entry?;
        let ty = entry.file_type()?;
        let to = to.join(entry.file_name());
        if ty.is_dir() {
            jobs.push(tokio::spawn(async move {
                tokio::fs::create_dir(&to).await?;
                copy_dir_boxed(entry.path(), to).await
            }));
        } else if ty.is_file() {
            file_futures.push(async move {
                tokio::fs::copy(entry.path(), to).await?;
                Ok::<_, anyhow::Error>(())
            });
        }
    }

    for future in file_futures {
        jobs.push(tokio::spawn(future));
    }

    for job in jobs {
        job.await??;
    }

    Ok(())
}

pub struct PreparedApp<'a> {
    bundler: &'a dyn Bundler,
    server: Option<(Child, String)>,
    test_dir: tempfile::TempDir,
    counter: usize,
}

impl<'a> PreparedApp<'a> {
    pub async fn new(bundler: &'a dyn Bundler, template_dir: PathBuf) -> Result<PreparedApp<'a>> {
        let test_dir = tempfile::tempdir()?;

        tokio::fs::create_dir_all(&test_dir).await?;
        copy_dir(template_dir, test_dir.path().to_path_buf()).await?;

        Ok(Self {
            bundler,
            server: None,
            test_dir,
            counter: 0,
        })
    }

    pub fn counter(&mut self) -> usize {
        self.counter += 1;
        self.counter
    }

    pub fn start_server(&mut self) -> Result<()> {
        assert!(self.server.is_none(), "Server already started");

        self.server = Some(self.bundler.start_server(self.test_dir.path())?);

        Ok(())
    }

    pub async fn with_page(self, browser: &Browser) -> Result<PageGuard<'a>> {
        let server = self.server.as_ref().context("Server must be started")?;
        let page = browser.new_page("about:blank").await?;
        // Bindings survive page reloads. Set them up as early as possible.
        add_binding(&page).await?;

        let mut errors = page.event_listener::<EventExceptionThrown>().await?;
        let binding_events = page.event_listener::<EventBindingCalled>().await?;
        let mut network_response_events = page.event_listener::<EventResponseReceived>().await?;

        let destination = Url::parse(&server.1)?.join(self.bundler.get_path())?;
        // We can't use page.goto() here since this will wait for the naviation to be
        // completed. A naviation would be complete when all sync script are
        // evaluated, but the page actually can rendered earlier without JavaScript
        // needing to be evaluated.
        // So instead we navigate via JavaScript and wait only for the HTML response to
        // be completed.
        page.evaluate_expression(format!("window.location='{destination}'"))
            .await?;

        // Wait for HTML response completed
        loop {
            match network_response_events.next().await {
                Some(event) => {
                    if event.response.url == destination.as_str() {
                        break;
                    }
                }
                None => return Err(anyhow!("event stream ended too early")),
            }
        }

        // Make sure no runtime errors occurred when loading the page
        assert!(errors.next().now_or_never().is_none());

        let page_guard = PageGuard::new(page, binding_events, errors, self);

        Ok(page_guard)
    }

    pub fn stop_server(&mut self) -> Result<()> {
        let mut proc = self.server.take().expect("Server never started").0;
        stop_process(&mut proc)?;
        Ok(())
    }

    pub fn path(&self) -> &Path {
        self.test_dir.path()
    }
}

impl<'a> Drop for PreparedApp<'a> {
    fn drop(&mut self) {
        if let Some(mut server) = self.server.take() {
            stop_process(&mut server.0).expect("failed to stop process");
        }
    }
}

/// Adds benchmark-specific bindings to the page.
async fn add_binding(page: &Page) -> Result<()> {
    page.execute(AddBindingParams::new(BINDING_NAME)).await?;
    Ok(())
}

#[cfg(unix)]
fn stop_process(proc: &mut Child) -> Result<()> {
    use std::time::Duration;

    use nix::{
        sys::signal::{kill, Signal},
        unistd::Pid,
    };
    use owo_colors::OwoColorize;

    const KILL_DEADLINE: Duration = Duration::from_secs(5);
    const KILL_DEADLINE_CHECK_STEPS: u32 = 10;

    let pid = Pid::from_raw(proc.id() as _);
    match kill(pid, Signal::SIGINT) {
        Ok(()) => {
            let expire = std::time::Instant::now() + KILL_DEADLINE;
            while let Ok(None) = proc.try_wait() {
                if std::time::Instant::now() > expire {
                    break;
                }
                std::thread::sleep(KILL_DEADLINE / KILL_DEADLINE_CHECK_STEPS);
            }
            if let Ok(None) = proc.try_wait() {
                eprintln!(
                    "{event_type} - process {pid} did not exit after SIGINT, sending SIGKILL",
                    event_type = "error".red(),
                    pid = pid
                );
                kill_process(proc)?;
            }
        }
        Err(_) => {
            eprintln!(
                "{event_type} - failed to send SIGINT to process {pid}, sending SIGKILL",
                event_type = "error".red(),
                pid = pid
            );
            kill_process(proc)?;
        }
    }
    Ok(())
}

#[cfg(not(unix))]
fn stop_process(proc: &mut Child) -> Result<()> {
    kill_process(proc)
}

fn kill_process(proc: &mut Child) -> Result<()> {
    proc.kill()?;
    proc.wait()?;
    Ok(())
}
