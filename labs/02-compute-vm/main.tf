resource "google_project_service" "compute" {
  service            = "compute.googleapis.com"
  disable_on_destroy = false
}

resource "google_project_service" "iap" {
  service            = "iap.googleapis.com"
  disable_on_destroy = false
}

resource "google_compute_network" "sandbox" {
  name                    = "sandbox-vpc"
  auto_create_subnetworks = false
  depends_on              = [google_project_service.compute]
}

resource "google_compute_subnetwork" "sandbox" {
  name          = "sandbox-subnet"
  network       = google_compute_network.sandbox.id
  ip_cidr_range = "10.10.0.0/24"
  region        = var.region
}

# Allow SSH only from Google's Identity-Aware Proxy range - no public IP needed.
resource "google_compute_firewall" "iap_ssh" {
  name          = "allow-iap-ssh"
  network       = google_compute_network.sandbox.id
  source_ranges = ["35.235.240.0/20"]

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }
}

resource "google_compute_instance" "vm" {
  name         = "sandbox-vm"
  machine_type = "e2-micro" # free tier: one e2-micro per month
  zone         = "${var.region}-a"

  boot_disk {
    initialize_params {
      image = "debian-cloud/debian-12"
      size  = 10
      type  = "pd-standard" # free tier only covers standard disks
    }
  }

  network_interface {
    subnetwork = google_compute_subnetwork.sandbox.id
    # no access_config block = no public IP
  }

  metadata = {
    enable-oslogin = "TRUE"
  }

  shielded_instance_config {
    enable_secure_boot = true
  }
}
