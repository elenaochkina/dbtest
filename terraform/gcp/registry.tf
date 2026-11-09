resource "google_artifact_registry_repository" "dbtest" {
  repository_id = "dbtest"
  location      = var.region
  format        = "DOCKER"

  cleanup_policies {
    id     = "expire-untagged"
    action = "DELETE"
    condition {
      tag_state  = "UNTAGGED"
      older_than = "604800s"
    }
  }
}
