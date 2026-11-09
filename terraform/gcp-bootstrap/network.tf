# AlloyDB clusters attach to this VPC through Private Services Access.
# dbtest connects over each instance's public IP; the VPC carries no traffic.
resource "google_compute_network" "dbtest" {
  name                    = "dbtest"
  auto_create_subnetworks = false

  depends_on = [google_project_service.apis]
}

# Google assigns AlloyDB's private addresses from this range.
resource "google_compute_global_address" "alloydb" {
  name          = "dbtest-alloydb"
  purpose       = "VPC_PEERING"
  address_type  = "INTERNAL"
  prefix_length = 20
  network       = google_compute_network.dbtest.id
}

resource "google_service_networking_connection" "alloydb" {
  network                 = google_compute_network.dbtest.id
  service                 = "servicenetworking.googleapis.com"
  reserved_peering_ranges = [google_compute_global_address.alloydb.name]
}
