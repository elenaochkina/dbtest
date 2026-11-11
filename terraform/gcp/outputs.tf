output "registry" {
  value = local.registry
}

# terraform -chdir=terraform/gcp output -raw worker_env >> .env
output "worker_env" {
  description = "Shell exports for the worker"
  value       = <<-EOT
    export GCP_CLOUDRUN_PROBE_JOB=${google_cloud_run_v2_job.harness["probe"].name}
    export GCP_CLOUDRUN_BENCH_JOB=${google_cloud_run_v2_job.harness["bench"].name}
  EOT
}
