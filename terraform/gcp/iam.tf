# Identity the jobs run as. The containers call no Google APIs, so it has no roles.
resource "google_service_account" "jobs" {
  account_id   = "dbtest-jobs"
  display_name = "dbtest jobs"
}
