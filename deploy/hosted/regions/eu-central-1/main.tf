# Hosted Perfuse in eu-central-1 (Frankfurt). Data residency: eea.
#
#   tofu init && tofu plan -var customer=example -var domain=perfuse.example.org \
#     -var certificate_arn=arn:aws:acm:eu-central-1:ACCOUNT:certificate/ID -var perfuse_version=v0.1.18
#
# State is kept in this Region too (an S3 bucket the operator creates once, with versioning and encryption): a state file
# names every resource and holds the database's endpoint, so it belongs with the data it describes.

terraform {
  required_version = ">= 1.6.0"
  # backend "s3" {
  #   bucket       = "perfuse-hosting-state-eu-central-1"
  #   key          = "CUSTOMER/terraform.tfstate"
  #   region       = "eu-central-1"
  #   encrypt      = true
  #   use_lockfile = true
  # }
}

variable "customer" { type = string }
variable "domain" { type = string }
variable "certificate_arn" { type = string }
variable "perfuse_version" { type = string }
variable "mllp_ports" {
  type    = list(number)
  default = []
}
variable "allowed_cidrs" {
  type    = list(string)
  default = []
}

module "perfuse" {
  source          = "../../modules/perfuse-region"
  customer        = var.customer
  region          = "eu-central-1"
  residency       = "eea"
  domain          = var.domain
  certificate_arn = var.certificate_arn
  perfuse_version = var.perfuse_version
  mllp_ports      = var.mllp_ports
  allowed_cidrs   = var.allowed_cidrs
}

output "address" { value = module.perfuse.address }
output "server" { value = module.perfuse.server }
output "where_the_data_is" { value = module.perfuse.where_the_data_is }
