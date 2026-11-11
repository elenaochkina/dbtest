provider "google" {
  project = var.project
  region  = var.region
}

data "google_compute_network" "dbtest" {
  name = "dbtest"
}
