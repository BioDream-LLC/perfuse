variable "customer" {
  description = "Short name of the hosted organisation (lower case, digits and dashes): it names every resource."
  type        = string
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,30}$", var.customer))
    error_message = "customer is 2 to 31 lower-case letters, digits and dashes, beginning with a letter."
  }
}

variable "region" {
  description = "The AWS Region everything lives in: eu-central-1 (Frankfurt), eu-west-1 (Ireland) or eu-west-2 (London)."
  type        = string
  validation {
    condition     = contains(["eu-central-1", "eu-west-1", "eu-west-2", "eu-west-3", "eu-north-1", "eu-south-1", "eu-south-2", "eu-central-2"], var.region)
    error_message = "This module is for the European and UK Regions only."
  }
}

variable "residency" {
  description = "What Perfuse's -residency is set to: eea, uk, ch, or region codes. Channels with cloud connectors outside it are refused."
  type        = string
}

variable "domain" {
  description = "The name the console and FHIR endpoint answer on (its DNS record points at the load balancer's address)."
  type        = string
}

variable "certificate_arn" {
  description = "An ACM certificate for var.domain, in var.region (DNS-validated by the organisation)."
  type        = string
  validation {
    condition     = can(regex("^arn:aws:acm:", var.certificate_arn))
    error_message = "certificate_arn is an ACM certificate ARN."
  }
}

variable "perfuse_version" {
  description = "The Perfuse release to install, such as v0.1.18."
  type        = string
}

variable "instance_type" {
  description = "The server's instance type (Graviton, arm64)."
  type        = string
  default     = "t4g.medium"
}

variable "db_instance_class" {
  description = "The PostgreSQL instance class."
  type        = string
  default     = "db.t4g.medium"
}

variable "db_multi_az" {
  description = "A standby database in a second Availability Zone of the same Region."
  type        = bool
  default     = false
}

variable "db_backup_days" {
  description = "Days of automated database backups (kept in the same Region; no cross-Region copy is made)."
  type        = number
  default     = 14
}

variable "backup_days" {
  description = "Days the backup bucket keeps Perfuse's own backups and old versions."
  type        = number
  default     = 35
}

variable "log_days" {
  description = "Days the server's logs and support-session logs are kept."
  type        = number
  default     = 90
}

variable "mllp_ports" {
  description = "TCP ports passed through to Perfuse for MLLP feeds (TLS is Perfuse's own)."
  type        = list(number)
  default     = []
}

variable "allowed_cidrs" {
  description = "Networks that may reach the console, FHIR endpoint and MLLP ports. Empty: anyone (the console still needs a sign-in)."
  type        = list(string)
  default     = []
}

variable "vpc_cidr" {
  description = "The VPC's address range."
  type        = string
  default     = "10.40.0.0/16"
}

variable "nat_gateway" {
  description = "A NAT gateway, so the server can reach partners' systems on the internet. Off: it reaches only AWS services in the Region (through endpoints) and the VPC."
  type        = bool
  default     = true
}

variable "private_endpoints" {
  description = "Reach Session Manager, CloudWatch Logs and Secrets Manager through VPC endpoints rather than the NAT gateway. Either way the traffic goes to the Region's own service endpoints; the endpoints keep it off the internet (about 40 USD a month). Needed when nat_gateway is false."
  type        = bool
  default     = true
}
