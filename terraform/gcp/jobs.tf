locals {
  registry = "${var.region}-docker.pkg.dev/${var.project}/${google_artifact_registry_repository.dbtest.repository_id}"
  jobs = {
    probe = { memory = "512Mi" }
    bench = { memory = "2Gi" }
  }
}

# One execution runs one task; each run passes its args as an override.
resource "google_cloud_run_v2_job" "harness" {
  for_each            = local.jobs
  name                = "dbtest-${each.key}"
  location            = var.region
  deletion_protection = false

  template {
    task_count = 1
    template {
      service_account = google_service_account.jobs.email
      timeout         = "21600s"
      max_retries     = 0

      containers {
        image = "${local.registry}/${each.key}:${var.image_tag}"
        resources {
          limits = { cpu = "1", memory = each.value.memory }
        }
      }

      vpc_access {
        network_interfaces {
          network    = data.google_compute_network.dbtest.id
          subnetwork = google_compute_subnetwork.jobs.id
        }
        egress = "PRIVATE_RANGES_ONLY"
      }
    }
  }
}
