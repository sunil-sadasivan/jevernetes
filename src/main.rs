use clap::{Parser as ClapParser, Subcommand};
use jevernetes::{
    drain::Strategy,
    events::{MAX_INPUT, Source, console},
    jev::{Jev, Usage},
    kubernetes::{self, Options},
    provider::ProviderKind,
    report::{Metrics, gap, write_report},
    runtime::{self, AnalyzeOptions, Sender},
    semantic::{Learner, Registry},
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
    after_help = "Dashboard, TUI, search, reviews and context remain in the explicitly legacy Python companion. See docs/migration.md."
)]
struct Cli {
    #[command(subcommand)]
    command: Command,
    #[arg(long, global = true)]
    offline: bool,
    #[arg(long, global = true)]
    json: bool,
    #[arg(long, global = true)]
    output: Option<PathBuf>,
    #[arg(long, global = true)]
    no_grouping: bool,
    /// Classification strategy: exact (default), off, drain, or semantic.
    #[arg(
        long,
        global = true,
        value_enum,
        default_value = "exact",
        conflicts_with = "no_grouping"
    )]
    grouping_strategy: Strategy,
    /// Global bound on Drain partitions (one template per partition).
    #[arg(long, global = true, default_value = "256", value_parser = clap::value_parser!(u16).range(1..=1024))]
    drain_capacity: u16,
    #[arg(long, global = true, value_enum, default_value = "typesafe")]
    risk_provider: ProviderKind,
    /// Required for OpenAI/Anthropic. TypeSafe defaults to jev-latest.
    #[arg(long, global = true)]
    model: Option<String>,
    /// Shadow learning only; requires semantic grouping. TypeSafe proposals are unsupported.
    #[arg(long, global = true, value_enum, default_value = "off")]
    template_provider: TemplateProvider,
    #[arg(long, global = true)]
    template_model: Option<String>,
    /// Separately operator-reviewed compiled rules; requires semantic grouping.
    #[arg(long, global = true)]
    template_rules: Option<PathBuf>,
    #[arg(long, global = true, default_value = "4", value_parser = clap::value_parser!(u8).range(2..=16))]
    template_min_support: u8,
    #[arg(long, global = true, default_value = "64", value_parser = clap::value_parser!(u16).range(1..=256))]
    template_capacity: u16,
    #[arg(long, global = true, default_value = "300", value_parser = clap::value_parser!(u16).range(1..=3600))]
    template_ttl: u16,
    #[arg(long, global = true, default_value = "10", value_parser = clap::value_parser!(u16).range(1..=1000))]
    template_max_requests: u16,
    #[arg(long, global = true, default_value = "0.05", value_parser = price)]
    template_max_cost: f64,
    #[arg(long, global = true, value_parser = price)]
    template_input_price: Option<f64>,
    #[arg(long, global = true, value_parser = price)]
    template_output_price: Option<f64>,
    /// Maximum events per request in every strategy; pending Drain misses classify independently.
    #[arg(long,global=true,default_value="8",value_parser=clap::value_parser!(u8).range(1..=64))]
    batch_size: u8,
    /// Request budget before retries, not a limit on individual classifications.
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
    #[arg(long,global=true,value_parser=price)]
    input_price: Option<f64>,
    #[arg(long,global=true,value_parser=price)]
    output_price: Option<f64>,
}
#[derive(Clone, Copy, PartialEq, Eq, clap::ValueEnum)]
enum TemplateProvider {
    Off,
    Openai,
    Anthropic,
}
impl Cli {
    fn model(&self) -> String {
        self.model.clone().unwrap_or_else(|| "jev-latest".into())
    }
    fn usage(&self) -> Usage {
        Usage::new(
            self.input_price.unwrap_or(0.042),
            self.output_price.unwrap_or(0.0),
        )
    }
    fn validate(&self) -> Result<(), &'static str> {
        jevernetes::provider::validate_model(&self.model())?;
        if let Some(model) = &self.template_model {
            jevernetes::provider::validate_model(model)?;
        }
        if self.risk_provider != ProviderKind::Typesafe
            && (self.model.is_none() || self.input_price.is_none() || self.output_price.is_none())
        {
            return Err(
                "OpenAI/Anthropic require explicit --model, --input-price and --output-price",
            );
        }
        if self.template_rules.is_some()
            && (self.grouping_strategy != Strategy::Semantic || self.no_grouping)
        {
            return Err("Reviewed template rules require --grouping-strategy semantic");
        }
        if self.template_provider != TemplateProvider::Off {
            if self.offline || self.grouping_strategy != Strategy::Semantic || self.no_grouping {
                return Err(
                    "Template providers require online --grouping-strategy semantic (shadow only)",
                );
            }
            if self.template_model.is_none()
                || self.template_input_price.is_none()
                || self.template_output_price.is_none()
            {
                return Err(
                    "Template provider requires explicit --template-model, --template-input-price and --template-output-price",
                );
            }
        } else if self.template_model.is_some()
            || self.template_input_price.is_some()
            || self.template_output_price.is_some()
        {
            return Err("Template model/prices require a template provider");
        } else if self.grouping_strategy == Strategy::Semantic
            && !self.offline
            && self.template_rules.is_none()
        {
            return Err(
                "Online semantic mode requires --template-rules or --template-provider openai or anthropic",
            );
        }
        Ok(())
    }
    fn clients(
        &self,
        metrics: &Arc<Mutex<Metrics>>,
    ) -> Result<(Option<Jev>, Option<Learner>), &'static str> {
        let risk = if self.offline {
            None
        } else {
            Some(Jev::new_for(
                self.risk_provider,
                &self.risk_provider.credential()?,
                self.model(),
                self.usage(),
            )?)
        };
        let template = match self.template_provider {
            TemplateProvider::Off => None,
            TemplateProvider::Openai => Some(ProviderKind::Openai),
            TemplateProvider::Anthropic => Some(ProviderKind::Anthropic),
        };
        let learner = if let Some(provider) = template {
            let client = Jev::new_for(
                provider,
                &provider.credential()?,
                self.template_model.clone().expect("validated model"),
                Usage::new(
                    self.template_input_price.expect("validated price"),
                    self.template_output_price.expect("validated price"),
                ),
            )?;
            Some(Learner::new(
                Registry::new(
                    usize::from(self.template_capacity),
                    usize::from(self.template_min_support),
                    Duration::from_secs(u64::from(self.template_ttl)),
                    metrics.clone(),
                )?,
                client,
                jevernetes::controller::Contract::provider(self.risk_provider, self.model()),
                u64::from(self.template_max_requests),
                self.template_max_cost,
                metrics.clone(),
            )?)
        } else {
            None
        };
        Ok((risk, learner))
    }
}
#[derive(Subcommand)]
enum Command {
    /// Persist judgments, apply confidence policy and durably notify while following Kubernetes.
    Controller(ControllerArgs),
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

#[derive(Clone, Copy, clap::ValueEnum)]
enum SinkKind {
    Stdout,
    Webhook,
}
#[derive(clap::Args)]
struct ControllerArgs {
    /// SQLite file in an existing private writable directory. One process/replica only.
    #[arg(long)]
    state: PathBuf,
    /// JSON Policy; unknown fields and invalid thresholds are rejected.
    #[arg(long)]
    policy: Option<PathBuf>,
    #[arg(long, value_enum, default_value = "stdout")]
    sink: SinkKind,
    #[arg(long, default_value = "0.0.0.0:9090")]
    listen: std::net::SocketAddr,
    #[arg(long, default_value="300", value_parser=clap::value_parser!(i64).range(1..=604800))]
    verdict_ttl: i64,
    /// Bypass verdict reads (including Drain); exact within-batch grouping remains enabled.
    #[arg(long)]
    rescore: bool,
    #[arg(long)]
    namespace: String,
    #[arg(long)]
    selector: Option<String>,
    #[arg(long, default_value="64",value_parser=positive)]
    max_streams: usize,
    #[arg(long,default_value="1h",value_parser=since)]
    since: i64,
    #[arg(long,default_value="500",value_parser=clap::value_parser!(i64).range(0..))]
    tail: i64,
    #[arg(long, default_value = "0")]
    duration: u64,
}
async fn run_controller(
    cli: &Cli,
    args: &ControllerArgs,
    rules: Option<jevernetes::reviewed::Rules>,
) -> Result<i32, &'static str> {
    use jevernetes::controller::{
        Contract, Controller, health,
        policy::Policy,
        sink::{Sink, Stdout, Webhook},
        store::Store,
    };
    use std::sync::atomic::Ordering;
    if cli.no_grouping || cli.grouping_strategy == Strategy::Off || cli.json {
        return Err(
            "Controller requires grouping; use --output for final reports and JSONL notifications on stdout",
        );
    }
    if let Some(output) = &cli.output {
        jevernetes::controller::paths::validate(&args.state, output)?;
    }
    let metrics = Arc::new(Mutex::new(Metrics::default()));
    let (client, learner) = cli.clients(&metrics)?;
    let policy = if let Some(path) = &args.policy {
        use std::io::Read;
        let file = std::fs::File::open(path).map_err(|_| "Cannot read controller policy")?;
        let mut data = Vec::new();
        file.take(16385)
            .read_to_end(&mut data)
            .map_err(|_| "Cannot read controller policy")?;
        if data.len() > 16384 {
            return Err("Controller policy exceeds 16 KiB");
        }
        serde_json::from_slice::<Policy>(&data).map_err(|_| "Invalid controller policy JSON")?
    } else {
        Policy::default()
    };
    policy.validate()?;
    let controller = Controller::new(
        Store::open(&args.state)?,
        Contract::provider(cli.risk_provider, cli.model()),
        policy,
        args.verdict_ttl,
        args.rescore,
        metrics.clone(),
    )?;
    let sink: Arc<dyn Sink> = match args.sink {
        SinkKind::Stdout => Arc::new(Stdout::new()?),
        SinkKind::Webhook => Arc::new(Webhook::from_env()?),
    };
    let usage = cli.usage();
    let options = Options {
        context: None,
        namespace: Some(args.namespace.clone()),
        selector: args.selector.clone(),
        since: args.since,
        tail: args.tail,
        max_bytes: MAX_INPUT,
        max_streams: args.max_streams,
        previous: false,
        in_cluster: true,
    };
    let (kube, cluster) = kubernetes::connect(&options).await?;
    let listener = tokio::net::TcpListener::bind(args.listen)
        .await
        .map_err(|_| "Cannot bind controller health listener")?;
    let stop = CancellationToken::new();
    let worker_stop = CancellationToken::new();
    let signals = signal(stop.clone())?;
    let fault_monitor = tokio::spawn({
        let c = controller.clone();
        let stop = stop.clone();
        async move {
            c.fault.cancelled().await;
            stop.cancel();
        }
    });
    let delivery = tokio::spawn({
        let c = controller.clone();
        let s = worker_stop.clone();
        async move {
            c.deliver(sink, s).await;
        }
    });
    let health_task = tokio::spawn(health::serve(
        listener,
        controller.clone(),
        worker_stop.clone(),
    ));
    let timer = tokio::spawn({
        let stop = stop.clone();
        let duration = args.duration;
        async move {
            if duration > 0 {
                tokio::time::sleep(Duration::from_secs(duration)).await;
                stop.cancel();
            }
        }
    });
    let (tx, rx) = mpsc::channel(cli.queue_size);
    let opts = AnalyzeOptions {
        batch_size: cli.batch_size.into(),
        max_batches: cli.max_batches as u64,
        max_cost: cli.max_cost,
        grouping: cli.grouping_strategy,
        drain_capacity: usize::from(cli.drain_capacity),
        retain: cli.retain_events,
        max_events: cli.max_events as u64,
        live: true,
        print_events: false,
    };
    let mut consumer = tokio::spawn(runtime::analyze_with_templates(
        rx,
        metrics.clone(),
        client,
        opts,
        stop.clone(),
        Some(controller.clone()),
        runtime::Templates {
            learner,
            reviewed: rules.map(|r| {
                (
                    r,
                    usize::from(cli.template_capacity),
                    Duration::from_secs(u64::from(cli.template_ttl)),
                )
            }),
        },
    ));
    eprintln!("[controller] advisory monitoring started; coverage is partial; one local writer");
    let started = Instant::now();
    let mut producer = tokio::spawn(kubernetes::live(
        kube,
        cluster,
        options,
        Sender {
            tx,
            metrics: metrics.clone(),
        },
        stop.clone(),
    ));
    // A failed analysis task must also stop otherwise healthy log streams.
    let (producer_result, result) = tokio::select! {
        collected = &mut producer => {
            stop.cancel();
            controller.ready.store(false, Ordering::Release);
            (collected, consumer.await)
        }
        analyzed = &mut consumer => {
            stop.cancel();
            controller.ready.store(false, Ordering::Release);
            (producer.await, analyzed)
        }
    };
    worker_stop.cancel();
    let _ = delivery.await;
    let _ = health_task.await;
    let interrupted = signals.is_finished();
    signals.abort();
    timer.abort();
    fault_monitor.abort();
    producer_result.map_err(|_| "Controller collection task failed")?;
    let (report, client) = result.map_err(|_| "Controller analysis task failed")?;
    let usage = client.map(|c| c.usage).unwrap_or(usage);
    let value = report.value(
        &metrics,
        &usage,
        json!({"kind":"controller","namespace":args.namespace,"selector":args.selector}),
        cli.offline,
        true,
        started.elapsed().as_secs_f64(),
    );
    if let Some(path) = &cli.output {
        jevernetes::controller::paths::validate(&args.state, path)?;
        write_report(path, &value)?;
    }
    eprintln!(
        "[controller] stopped; events={} state_failures={} pending={} dead={} coverage_gaps={}",
        report.total,
        metrics.lock().expect("metrics").store_failures,
        value["metrics"]["outbox_pending"],
        value["metrics"]["outbox_dead"],
        value["summary"]["coverage_gaps"]
    );
    Ok(if controller.fault.is_cancelled() {
        1
    } else if interrupted {
        130
    } else {
        2
    })
}

fn signal(stop: CancellationToken) -> Result<tokio::task::JoinHandle<()>, &'static str> {
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
            stop.cancel();
        }))
    }
    #[cfg(not(unix))]
    {
        Ok(tokio::spawn(async move {
            let _ = tokio::signal::ctrl_c().await;
            stop.cancel();
        }))
    }
}

async fn run(cli: Cli) -> Result<i32, &'static str> {
    cli.validate()?;
    // Validate the entire artifact before credentials, input, state, or cluster access.
    let rules = cli
        .template_rules
        .as_ref()
        .map(|p| jevernetes::reviewed::Rules::load(p, jevernetes::controller::now()))
        .transpose()?;
    if let Command::Controller(args) = &cli.command {
        return run_controller(&cli, args, rules).await;
    }
    let started = Instant::now();
    let metrics = Arc::new(Mutex::new(Metrics::default()));
    let stop = CancellationToken::new();
    let usage = cli.usage();
    let (client, learner) = cli.clients(&metrics)?;
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
        grouping: if cli.no_grouping {
            Strategy::Off
        } else {
            cli.grouping_strategy
        },
        drain_capacity: usize::from(cli.drain_capacity),
        retain: if live {
            cli.retain_events
        } else {
            cli.max_events
        },
        max_events: cli.max_events as u64,
        live,
        print_events: live && !cli.json,
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
    let signals = signal(stop.clone())?;
    let consumer = tokio::spawn(runtime::analyze_with_templates(
        rx,
        metrics.clone(),
        client,
        opts,
        stop.clone(),
        None,
        runtime::Templates {
            learner,
            reviewed: rules.map(|r| {
                (
                    r,
                    usize::from(cli.template_capacity),
                    Duration::from_secs(u64::from(cli.template_ttl)),
                )
            }),
        },
    ));
    let producer_stop = stop.clone();
    let producer_metrics = metrics.clone();
    let producer = tokio::spawn(async move {
        match cli.command {
            Command::Controller(_) => unreachable!("controller dispatched separately"),
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
        if live {
            loop {
                tokio::select! {_=status_stop.cancelled()=>break,_=tokio::time::sleep(Duration::from_secs(10))=>{let m=status_metrics.lock().expect("metrics lock");eprintln!("[tail] streams={} queue={} high_water={} dropped={} reconnects={} gaps={}",m.active_streams,m.queue_depth,m.queue_high_water,m.dropped,m.reconnects,m.coverage_gaps);}}
            }
        }
    });
    if producer.await.is_err() {
        gap(&metrics, &Source::new(), "error", "Collector task failed");
        stop.cancel();
    }
    let (report, client) = consumer.await.map_err(|_| "Analysis task failed")?;
    if signals.is_finished() {
        interrupted = true;
    }
    signals.abort();
    status.abort();
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
    if cli.json {
        println!(
            "{}",
            serde_json::to_string_pretty(&value).map_err(|_| "Cannot serialize report")?
        );
    } else {
        println!(
            "jevernetes · {}\n{} events / {} lines · important {} · uncertain {} · unknown {}\ncoverage gaps {} · dropped {} · estimated ${:.8}",
            value["mode"].as_str().unwrap_or("provider"),
            report.total,
            report.lines,
            report.counts.get("important").unwrap_or(&0),
            report.counts.get("uncertain").unwrap_or(&0),
            report.counts.get("unknown").unwrap_or(&0),
            value["summary"]["coverage_gaps"],
            value["metrics"]["dropped"],
            value["summary"]["total_estimated_cost_usd"]
                .as_f64()
                .unwrap_or(usage.estimated_cost_usd)
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
