# The YAML file is the source of truth, so an admin who does not use Terraform
# can create the same role with: gcloud iam roles create dbtest --project=<project> --file=dbtest-role.yaml
locals {
  role = yamldecode(file("${path.module}/dbtest-role.yaml"))
}

resource "google_project_iam_custom_role" "dbtest" {
  role_id     = "dbtest"
  title       = local.role.title
  description = local.role.description
  permissions = local.role.includedPermissions

  depends_on = [google_project_service.apis]
}

resource "google_service_account" "dbtest" {
  account_id   = "dbtest"
  display_name = "dbtest"
  description  = "Creates, disrupts and deletes dbtest Cloud SQL instances and AlloyDB clusters"

  depends_on = [google_project_service.apis]
}

resource "google_project_iam_member" "dbtest" {
  project = var.project
  role    = google_project_iam_custom_role.dbtest.id
  member  = google_service_account.dbtest.member
}

resource "google_service_account_iam_member" "impersonate" {
  for_each = toset(var.trusted_principals)

  service_account_id = google_service_account.dbtest.name
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = each.value
}
