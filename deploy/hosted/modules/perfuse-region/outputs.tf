output "address" {
  description = "The load balancer's name: point var.domain at it (a CNAME, or an alias record in Route 53)."
  value       = aws_lb.main.dns_name
}

output "backup_bucket" {
  description = "Where Perfuse's own files are backed up, in the Region."
  value       = aws_s3_bucket.backups.bucket
}

output "database" {
  description = "The PostgreSQL endpoint (private)."
  value       = aws_db_instance.main.address
}

output "server" {
  description = "The server's instance, for Session Manager: aws ssm start-session --region <region> --target <id>."
  value       = aws_instance.server.id
}

output "where_the_data_is" {
  description = "A statement for the Data protection screen (Settings → Data → Where the data is kept)."
  value = join(" ", [
    "AWS ${var.region}: server, PostgreSQL and its automated backups (${var.db_backup_days} days), file backups in",
    "${aws_s3_bucket.backups.bucket} (${var.backup_days} days), logs and support-session records (${var.log_days} days),",
    "all encrypted with a key held in ${var.region}; nothing replicated to another Region; Perfuse runs with -residency ${var.residency}.",
  ])
}
