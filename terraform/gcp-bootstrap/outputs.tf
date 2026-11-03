# Each user passes this to gcloud --impersonate-service-account.
output "service_account_email" {
  value = google_service_account.dbtest.email
}
