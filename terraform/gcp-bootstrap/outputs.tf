# Each user passes this to gcloud --impersonate-service-account.
output "service_account_email" {
  value = google_service_account.dbtest.email
}

# Each user sets GCP_ALLOYDB_NETWORK to this; the worker defaults to dbtest.
output "alloydb_network" {
  value = google_compute_network.dbtest.name
}
