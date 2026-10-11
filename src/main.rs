use clap::{Parser as ClapParser, Subcommand};
use jevernetes::{
    events::{MAX_INPUT, Source, console},
    jev::{Jev, Usage},
    kubernetes::{self, Options},
    progress::Progress,
    report::{Metrics, gap, write_report},
    runtime::{self, AnalyzeOptions, Sender},
    tui,
};
use serde_json::json;
use std::{
    path::PathBuf,
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};
use tokio::{io::BufReader, sync::mpsc};
use tokio_util::sync::CancellationToken;
fn positive(s: &str) -> Result<usize, String> {
    s.parse::<usize>()
        .ok()
        .filter(|n| *n > 0 && *n <= 1_000_000)
        .ok_or_else(|| "must be between 1 and 1000000".into())
}
fn price(s: &str) -> Result<f64, String> {
    s.parse::<f64>()
        .ok()
        .filter(|n| n.is_finite() && *n >= 0.0 && *n <= 1_000_000.0)
        .ok_or_else(|| "must be finite, nonnegative and at most 1000000".into())
}
fn since(s: &str) -> Result<i64, String> {
    let (n, mult) = match s.as_bytes().last() {
        Some(b's') => (&s[..s.len() - 1], 1),
        Some(b'm') => (&s[..s.len() - 1], 60),
        Some(b'h') => (&s[..s.len() - 1], 3600),
        Some(b'd') => (&s[..s.len() - 1], 86400),
        _ => (s, 1),
    };
    n.parse::<i64>()
        .ok()
        .filter(|v| *v >= 0)
        .and_then(|n| n.checked_mul(mult))
        .ok_or_else(|| "use a nonnegative duration such as 1h, 30m or 60s".into())
}
#[derive(ClapParser)]
#[command(
    name = "jevernetes",
    version,
    about = "Bounded read-only log analysis; Rust runtime",
    after_help = "Use --tui for interactive tabs, event details and exact/Jev search. Dashboard, reviews and extra context remain in the legacy Python companion."
)]
struct Cli {
    #[command(subcommand)]
    command: Command,
    #[arg(long, global = true)]
    offline: bool,
    #[arg(long, global = true)]
    json: bool,
    /// Interactive terminal with filters, event details and exact/Jev search.
    #[arg(long, global = true, conflicts_with = "json")]
    tui: bool,
    #[arg(long, global = true)]
    output: Option<PathBuf>,
    #[arg(long, global = true)]
    no_grouping: bool,
    #[arg(long, global = true, default_value = "jev-latest")]
    model: String,
    #[arg(long,global=true,default_value="8",value_parser=clap::value_parser!(u8).range(1..=64))]
    batch_size: u8,
    #[arg(long,global=true,default_value="500",value_parser=positive)]
    max_batches: usize,
    #[arg(long,global=true,default_value="100000",value_parser=positive)]
    max_events: usize,
    #[arg(long,global=true,default_value="1024",value_parser=positive)]
    queue_size: usize,
    #[arg(long,global=true,default_value="2000",value_parser=positive)]
    retain_events: usize,
    #[arg(long,global=true,default_value="0.25",value_parser=price)]
    max_cost: f64,
    #[arg(long,global=true,default_value="0.042",value_parser=price)]
    input_price: f64,
    #[arg(long,global=true,default_value="0",value_parser=price)]
    output_price: f64,
}
#[derive(Subcommand)]
enum Command {
    /// Analyze text/JSONL, gzip files, or '-' for stdin.
    Files {
        #[arg(required=true,num_args=1..)]
        paths: Vec<String>,
        #[arg(long,default_value_t=MAX_INPUT,value_parser=clap::value_parser!(u64).range(1..=1_073_741_824))]
        max_file_bytes: u64,
    },
    /// Read Kubernetes snapshots or continuously discover and follow running containers.
    #[command(alias = "kubernetes")]
    K8s {
        #[arg(long, short = 'f', alias = "live")]
        follow: bool,
        #[arg(long, conflicts_with = "in_cluster")]
        context: Option<String>,
        #[arg(long)]
        in_cluster: bool,
        #[arg(long)]
        namespace: Option<String>,
        #[arg(long)]
        selector: Option<String>,
        #[arg(long,default_value="1h",value_parser=since)]
        since: i64,
        #[arg(long,default_value="500",value_parser=clap::value_parser!(i64).range(0..))]
        tail: i64,
        #[arg(long,default_value_t=MAX_INPUT,value_parser=clap::value_parser!(u64).range(1..=1_073_741_824))]
        max_bytes: u64,
        #[arg(long,default_value="64",value_parser=positive)]
        max_streams: usize,
        #[arg(long)]
        no_previous: bool,
        #[arg(long, default_value = "0")]
        duration: u64,
    },
}
fn signal(
    stop: CancellationToken,
    interrupted: CancellationToken,
) -> Result<tokio::task::JoinHandle<()>, &'static str> {
    // Install handlers before starting collection, not on a later task poll.
    #[cfg(unix)]
    {
        let mut term = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
            .map_err(|_| "Cannot install SIGTERM handler")?;
        let mut interrupt =
            tokio::signal::unix::signal(tokio::signal::unix::SignalKind::interrupt())
                .map_err(|_| "Cannot install SIGINT handler")?;
        Ok(tokio::spawn(async move {
            tokio::select! { _ = term.recv() => (), _ = interrupt.recv() => () }
            interrupted.cancel();
            stop.cancel();
        }))
    }
    #[cfg(not(unix))]
    {
        Ok(tokio::spawn(async move {
            let _ = tokio::signal::ctrl_c().await;
            interrupted.cancel();
            stop.cancel();
        }))
    }
}

fn key() -> Result<String, &'static str> {
    for name in ["TYPESAFE_API_KEY", "TYPESAFEAI_API_KEY"] {
        if let Ok(s) = std::env::var(name)
            && !s.trim().is_empty()
        {
            return Ok(s);
        }
    }
    if let Ok(path) = std::env::var("TYPESAFE_API_KEY_FILE") {
        use std::io::Read;
        let file = std::fs::File::open(path).map_err(|_| "Cannot read TYPESAFE_API_KEY_FILE")?;
        let mut bytes = Vec::new();
        file.take(16385)
            .read_to_end(&mut bytes)
            .map_err(|_| "Cannot read TYPESAFE_API_KEY_FILE")?;
        if bytes.len() > 16384 {
            return Err("Jev key file exceeds limit");
        }
        let value = String::from_utf8(bytes).map_err(|_| "Invalid Jev key file")?;
        if !value.trim().is_empty() {
            return Ok(value);
        }
    }
    Err(
        "Set TYPESAFE_API_KEY, TYPESAFEAI_API_KEY or TYPESAFE_API_KEY_FILE; use --offline for local rules",
    )
}
async fn run(cli: Cli) -> Result<i32, &'static str> {
    if cli.tui {
        tui::validate(
            matches!(&cli.command, Command::Files { paths, .. } if paths.iter().any(|p| p == "-")),
        )?;
    }
    let started = Instant::now();
    let metrics = Arc::new(Mutex::new(Metrics::default()));
    let stop = CancellationToken::new();
    let usage = Usage::new(cli.input_price, cli.output_price);
    let client = if cli.offline {
        None
    } else {
        Some(Jev::new(&key()?, cli.model.clone(), usage.clone())?)
    };
    let (tx, rx) = mpsc::channel(cli.queue_size);
    let sender = Sender {
        tx,
        metrics: metrics.clone(),
    };
    let live = matches!(&cli.command, Command::K8s { follow: true, .. });
    let opts = AnalyzeOptions {
        batch_size: usize::from(cli.batch_size),
        max_batches: cli.max_batches as u64,
        max_cost: cli.max_cost,
        grouping: !cli.no_grouping,
        retain: if live {
            cli.retain_events
        } else {
            cli.max_events
        },
        max_events: cli.max_events as u64,
        live,
        print_events: live && !cli.json && !cli.tui,
    };
    let progress = cli.tui.then(|| Progress::new(opts.retain, usage.clone()));
    let search_client = if cli.tui {
        client.as_ref().map(Jev::search_client)
    } else {
        None
    };
    let mut scope = json!({"kind":"files"});
    let mut interrupted = false;
    // Connect before spawning tasks; failures cannot leave an orphan consumer.
    let connection = if let Command::K8s {
        context,
        in_cluster,
        namespace,
        selector,
        since,
        tail,
        max_bytes,
        max_streams,
        no_previous,
        ..
    } = &cli.command
    {
        let options = Options {
            context: context.clone(),
            namespace: namespace.clone(),
            selector: selector.clone(),
            since: *since,
            tail: *tail,
            max_bytes: *max_bytes,
            max_streams: *max_streams,
            previous: !*no_previous,
            in_cluster: *in_cluster,
        };
        let (client, cluster) = kubernetes::connect(&options).await?;
        scope = json!({"context":cluster,"namespace":namespace,"selector":selector,"since_seconds":since,"tail":tail,"live":live});
        Some((client, cluster, options))
    } else {
        None
    };
    let screen = if cli.tui {
        Some(tui::Screen::enter()?)
    } else {
        None
    };
    let interrupt = CancellationToken::new();
    let signals = signal(stop.clone(), interrupt.clone())?;
    let consumer = tokio::spawn(runtime::analyze_observed(
        rx,
        metrics.clone(),
        client,
        opts,
        stop.clone(),
        progress.clone(),
    ));
    let ui = screen.map(|screen| {
        tokio::spawn(tui::browse(
            screen,
            progress.as_ref().expect("TUI progress").clone(),
            metrics.clone(),
            search_client,
            cli.offline,
            stop.clone(),
            interrupt.clone(),
        ))
    });
    let producer_stop = stop.clone();
    let producer_metrics = metrics.clone();
    let producer = tokio::spawn(async move {
        match cli.command {
            Command::Files {
                paths,
                max_file_bytes,
            } => {
                for path in paths {
                    if producer_stop.is_cancelled() {
                        break;
                    }
                    let source: Source =
                        serde_json::from_value(json!({"type":"file","path":path})).expect("source");
                    producer_metrics
                        .lock()
                        .expect("metrics lock")
                        .streams_started += 1;
                    if path == "-" {
                        runtime::ingest(
                            tokio::io::stdin(),
                            source,
                            max_file_bytes,
                            &sender,
                            &producer_stop,
                        )
                        .await;
                    } else {
                        match tokio::fs::File::open(&path).await {
                            Ok(file) => {
                                if path.ends_with(".gz") {
                                    let mut decoder =
                                        async_compression::tokio::bufread::GzipDecoder::new(
                                            BufReader::new(file),
                                        );
                                    decoder.multiple_members(true);
                                    runtime::ingest(
                                        decoder,
                                        source,
                                        max_file_bytes,
                                        &sender,
                                        &producer_stop,
                                    )
                                    .await;
                                } else {
                                    runtime::ingest(
                                        file,
                                        source,
                                        max_file_bytes,
                                        &sender,
                                        &producer_stop,
                                    )
                                    .await;
                                }
                            }
                            Err(_) => gap(
                                &producer_metrics,
                                &source,
                                "error",
                                "Cannot open input file",
                            ),
                        }
                    }
                }
            }
            Command::K8s {
                follow, duration, ..
            } => {
                let (client, cluster, options) = connection.expect("Kubernetes connection");
                let timer = if duration > 0 {
                    let stop = producer_stop.clone();
                    Some(tokio::spawn(async move {
                        tokio::time::sleep(Duration::from_secs(duration)).await;
                        stop.cancel();
                    }))
                } else {
                    None
                };
                if follow {
                    kubernetes::live(client, cluster, options, sender, producer_stop).await;
                } else {
                    kubernetes::snapshot(client, cluster, options, sender, producer_stop).await;
                }
                if let Some(timer) = timer {
                    timer.abort();
                }
            }
        }
    });
    let status_stop = stop.clone();
    let status_metrics = metrics.clone();
    let status = tokio::spawn(async move {
        if live && !cli.tui {
            loop {
                tokio::select! {_=status_stop.cancelled()=>break,_=tokio::time::sleep(Duration::from_secs(10))=>{let m=status_metrics.lock().expect("metrics lock");eprintln!("[tail] streams={} queue={} high_water={} dropped={} reconnects={} gaps={}",m.active_streams,m.queue_depth,m.queue_high_water,m.dropped,m.reconnects,m.coverage_gaps);}}
            }
        }
    });
    if producer.await.is_err() {
        gap(&metrics, &Source::new(), "error", "Collector task failed");
        stop.cancel();
    }
    let result = consumer.await;
    status.abort();
    if let Some(progress) = &progress {
        progress
            .lock()
            .expect("progress lock")
            .finish(if result.is_err() {
                "Failed"
            } else {
                "Complete"
            });
    }
    if result.is_err() {
        stop.cancel();
        interrupt.cancel();
    }
    let ui_result = match ui {
        Some(ui) => ui.await.unwrap_or(Err("Terminal UI task failed")),
        None => Ok(false),
    };
    interrupted |= interrupt.is_cancelled() || matches!(ui_result, Ok(true));
    signals.abort();
    let (report, client) = result.map_err(|_| "Analysis task failed")?;
    let usage = client.map(|c| c.usage).unwrap_or(usage);
    let value = report.value(
        &metrics,
        &usage,
        scope,
        cli.offline,
        live,
        started.elapsed().as_secs_f64(),
    );
    if let Some(path) = cli.output {
        write_report(&path, &value)?;
    }
    ui_result?;
    if cli.json {
        println!(
            "{}",
            serde_json::to_string_pretty(&value).map_err(|_| "Cannot serialize report")?
        );
    } else {
        println!(
            "jevernetes · {}\n{} events / {} lines · important {} · uncertain {} · unknown {}\ncoverage gaps {} · dropped {} · estimated ${:.8}",
            if cli.offline { "offline-rules" } else { "jev" },
            report.total,
            report.lines,
            report.counts.get("important").unwrap_or(&0),
            report.counts.get("uncertain").unwrap_or(&0),
            report.counts.get("unknown").unwrap_or(&0),
            value["summary"]["coverage_gaps"],
            value["metrics"]["dropped"],
            usage.estimated_cost_usd
        );
        if let Some(groups) = value["important_groups"].as_array() {
            for group in groups.iter().take(15) {
                println!(
                    "[{} / {}] ×{} {}",
                    group["severity"].as_str().unwrap_or("unknown"),
                    group["category"].as_str().unwrap_or("unknown"),
                    group["count"],
                    console(group["text"].as_str().unwrap_or_default())
                );
            }
        }
    }
    Ok(if interrupted {
        130
    } else if value["summary"]["complete_within_window"] == true {
        0
    } else {
        2
    })
}
#[tokio::main]
async fn main() {
    let cli = Cli::parse();
    let code = match run(cli).await {
        Ok(code) => code,
        Err(error) => {
            eprintln!("jevernetes: {error}");
            1
        }
    };
    std::process::exit(code);
}
