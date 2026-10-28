output "ssh" {
  value = "gcloud compute ssh ${google_compute_instance.vm.name} --zone ${google_compute_instance.vm.zone} --tunnel-through-iap"
}
