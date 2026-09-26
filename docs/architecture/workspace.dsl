/*
 * C4 model of bk-agent-stack-orchard (levels 1 to 3).
 *
 * Views:
 *   SystemContext         Level 1: the stack, its users, and external systems.
 *   Containers            Level 2: the deployable parts of the stack.
 *   ControllerComponents  Level 3: the components of the controller.
 *
 * The target design is docs/architecture/target-design.md.
 * Relationships are declared from the highest level to the lowest level. Because of this,
 * implied relationships do not replace the descriptions of higher-level relationships.
 */
workspace "bk-agent-stack-orchard" "C4 model of the Buildkite agent stack that runs each job on a temporary macOS VM through Orchard." {

    !identifiers hierarchical

    model {
        author = person "Pipeline author" "Writes Buildkite pipelines that run on macOS. Reads the job results."
        operator = person "Fleet operator" "Operates the Mac hosts, Orchard, and the controller."

        buildkite = softwareSystem "Buildkite" "SaaS CI control plane. Gives the Stacks API, the Agent API, and the web UI." {
            tags "External"
        }
        registry = softwareSystem "OCI registry" "Stores the macOS VM images." {
            tags "External"
        }
        sources = softwareSystem "Source hosts and remote caches" "Git hosts, package registries, and remote build caches that builds use." {
            tags "External"
        }
        kubernetes = softwareSystem "Kubernetes" "Reference deployment platform. Runs the controller and the Orchard controller. ArgoCD deploys them." {
            tags "External"
        }
        telemetry = softwareSystem "Telemetry backend" "OpenTelemetry Collector and the observability backend behind it." {
            tags "External"
        }

        stack = softwareSystem "bk-agent-stack-orchard" "Runs each Buildkite job on a temporary macOS VM on self-hosted Apple Silicon Macs." {

            controller = container "Controller" "Monitors the Buildkite queue. Creates one Orchard VM for each job. Deletes the VM when the job ends." "Go 1.27, distroless container image or binary" {
                config = component "Configuration" "Loads and validates the YAML file and the secret environment variables." "Go"
                health = component "Health server" "Serves the gRPC health service: liveness and readiness." "Go, gRPC"
                tick = component "Tick loop" "Runs the refresh, Reaper, Admission, and Launcher steps in sequence." "Go"
                stackRegistration = component "Stack" "Registers and deregisters the custom stack. Gives the dispatch_paused value." "Go"
                poller = component "Poller" "Lists the first page of scheduled jobs at each poll interval." "Go"
                inventory = component "Inventory" "Lists Orchard workers and VMs. Selects owned VMs by name prefix." "Go"
                jobStates = component "JobStates" "Gets the Buildkite job states of owned VMs in batches. Records the observation times." "Go"
                matcher = component "Matcher" "Pure function. Converts job agent tags to exactly one VM profile, or to a mismatch reason." "Go"
                admission = component "Admission" "Pure function. Selects the jobs that fit the free capacity and the unmatched jobs to fail." "Go"
                launcher = component "Launcher" "Reserves jobs, gets job acquisition tokens, and creates VMs." "Go"
                reaper = component "Reaper" "Pure decision function and executor. Deletes VMs, finishes jobs, and extends reservations." "Go"
                stacksClient = component "Stacks client" "Narrow interface over github.com/buildkite/stacksapi." "Go"
                orchardClient = component "Orchard client" "Narrow interface over the Orchard pkg/client package." "Go"
                otelSetup = component "Telemetry" "Configures the OpenTelemetry meter, tracer, and slog bridge." "Go, OpenTelemetry SDK"
            }

            orchardController = container "Orchard controller" "Keeps the VM and worker state. Schedules VMs on workers." "Go, container image or binary"
            orchardStore = container "Orchard state store" "Stores VMs, workers, and service accounts." "BadgerDB on persistent storage" {
                tags "Database"
            }
            orchardWorker = container "Orchard worker" "Registers the Mac host. Runs the VMs that the Orchard controller assigns." "Go, launchd on each Mac host"
            tart = container "Tart" "Pulls, clones, and runs macOS VMs with Apple Virtualization.framework. Softnet isolates the VM network." "Swift CLI on each Mac host"
            jobVm = container "Job VM" "Temporary macOS VM. Runs buildkite-agent for exactly one job." "macOS guest, buildkite-agent" {
                tags "Ephemeral"
            }
        }

        # Level 1 relationships
        author -> buildkite "Uploads pipelines. Reads job results and stack notifications."
        operator -> stack "Configures profiles. Operates the Mac hosts."
        operator -> telemetry "Reads metrics, traces, and logs."
        stack -> buildkite "Gets scheduled jobs. Runs and reports jobs." "HTTPS/JSON"
        stack -> registry "Pulls VM images." "OCI Distribution API over HTTPS"
        stack -> sources "Gets source code and cache entries during builds." "HTTPS"
        stack -> telemetry "Exports metrics, traces, and logs." "OTLP"
        kubernetes -> stack "Runs and probes the controller and the Orchard controller."

        # Level 2 relationships
        operator -> stack.controller "Configures profiles through the deployment platform."
        operator -> stack.orchardWorker "Installs and configures with Ansible."
        kubernetes -> stack.controller "Runs and probes liveness and readiness."
        kubernetes -> stack.orchardController "Runs the Orchard controller."
        stack.controller -> buildkite "Lists and reserves jobs. Gets tokens and job states. Finishes jobs. Sends notifications." "Stacks API, HTTPS/JSON"
        stack.controller -> stack.orchardController "Lists workers and VMs. Creates and deletes VMs. Reads VM events." "Orchard REST API, HTTPS/JSON"
        stack.controller -> telemetry "Exports metrics, traces, and logs." "OTLP"
        stack.orchardController -> stack.orchardStore "Reads and writes state."
        stack.orchardController -> telemetry "Exports metrics." "OTLP"
        stack.orchardWorker -> stack.orchardController "Registers. Gets VM assignments. Reports VM status." "gRPC, outbound from the Mac host"
        stack.orchardWorker -> stack.tart "Pulls, clones, configures, runs, stops, and deletes VMs." "CLI"
        stack.orchardWorker -> stack.jobVm "Runs the startup script." "Tart guest agent"
        stack.orchardWorker -> telemetry "Exports metrics." "OTLP"
        stack.tart -> registry "Pulls VM images." "OCI Distribution API over HTTPS"
        stack.tart -> stack.jobVm "Runs." "Virtualization.framework"
        stack.jobVm -> buildkite "Acquires its job. Sends logs, artifacts, and the result." "Agent API, HTTPS"
        stack.jobVm -> sources "Gets source code and cache entries." "HTTPS through Softnet"
        stack.jobVm -> telemetry "Exports agent traces when the profile enables it." "OTLP through a public ingest collector"

        # Level 3 relationships (controller components)
        operator -> stack.controller.config "Supplies the configuration file and secrets."
        kubernetes -> stack.controller.health "Probes liveness and readiness." "gRPC"
        stack.controller.config -> stack.controller.tick "Gives intervals, limits, and timeouts to."
        stack.controller.config -> stack.controller.matcher "Gives profiles to."
        stack.controller.health -> stack.controller.tick "Reads the last synchronization times from."
        stack.controller.tick -> stack.controller.stackRegistration "Reads dispatch_paused from."
        stack.controller.tick -> stack.controller.poller "Reads scheduled jobs from."
        stack.controller.tick -> stack.controller.inventory "Refreshes."
        stack.controller.tick -> stack.controller.jobStates "Refreshes."
        stack.controller.tick -> stack.controller.reaper "Runs for each owned VM."
        stack.controller.tick -> stack.controller.admission "Runs with fresh data."
        stack.controller.tick -> stack.controller.launcher "Runs with admitted and unmatched jobs."
        stack.controller.admission -> stack.controller.matcher "Matches jobs to profiles with."
        stack.controller.admission -> stack.controller.inventory "Reads workers and VMs from."
        stack.controller.reaper -> stack.controller.inventory "Reads owned VMs from."
        stack.controller.reaper -> stack.controller.jobStates "Reads job states from."
        stack.controller.reaper -> stack.controller.stacksClient "Finishes jobs, extends reservations, and sends notifications through."
        stack.controller.reaper -> stack.controller.orchardClient "Deletes VMs and reads VM events through."
        stack.controller.launcher -> stack.controller.stacksClient "Reserves jobs, gets tokens, and sends notifications through."
        stack.controller.launcher -> stack.controller.orchardClient "Creates VMs through."
        stack.controller.stackRegistration -> stack.controller.stacksClient "Registers the stack through."
        stack.controller.poller -> stack.controller.stacksClient "Lists scheduled jobs through."
        stack.controller.jobStates -> stack.controller.stacksClient "Gets job states through."
        stack.controller.inventory -> stack.controller.orchardClient "Lists workers and VMs through."
        stack.controller.stacksClient -> buildkite "Calls the Stacks API." "HTTPS/JSON"
        stack.controller.orchardClient -> stack.orchardController "Calls the Orchard REST API." "HTTPS/JSON"
        stack.controller.otelSetup -> telemetry "Exports metrics, traces, and logs." "OTLP"
    }

    views {
        systemContext stack "SystemContext" "Level 1: bk-agent-stack-orchard, its users, and the external systems." {
            include *
            # The pipeline author uses the stack only through Buildkite, so "include *" does not add the author.
            include author
            autoLayout lr
        }

        container stack "Containers" "Level 2: the deployable parts of bk-agent-stack-orchard." {
            include *
            autoLayout lr
        }

        component stack.controller "ControllerComponents" "Level 3: the components of the controller." {
            include *
            # Remove relationships that do not involve a controller component.
            exclude "operator -> telemetry"
            exclude "kubernetes -> stack.orchardController"
            exclude "stack.orchardController -> telemetry"
            autoLayout lr
        }

        styles {
            element "Element" {
                color #ffffff
            }
            element "Person" {
                shape Person
                background #08427b
            }
            element "Software System" {
                background #1168bd
            }
            element "Container" {
                background #438dd5
            }
            element "Component" {
                background #85bbf0
                color #000000
            }
            element "External" {
                background #999999
            }
            element "Database" {
                shape Cylinder
            }
            element "Ephemeral" {
                border Dashed
            }
        }
    }
}
