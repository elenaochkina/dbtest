# Cloud Run jobs attach here through Direct VPC egress and reach database private IPs over the peering.
resource "google_compute_subnetwork" "jobs" {
  name          = "dbtest-jobs"
  network       = data.google_compute_network.dbtest.id
  region        = var.region
  ip_cidr_range = "10.10.0.0/24"
}
