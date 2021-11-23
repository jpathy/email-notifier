variable "aws_region" {
  type    = string
  default = "eu-west-1"
}

variable "stack" {
  type        = string
  description = "stack name that will be used to generate the aws resource names"
  default     = "email-notifier"
}

variable "github_org" {
  type        = string
  description = "github username/orgname"
}

variable "github_repo" {
  type        = string
  description = "github repo whose actions runner will be able to deploy lambda using aws-actions/configure-aws-credentials"
}

variable "ses_domain_addresses" {
  type        = map(list(string))
  description = "map from domain name to list of addresses allowed to receive email,  addresses should conform to https://docs.aws.amazon.com/ses/latest/DeveloperGuide/receiving-email-receipt-rules.html"
}

variable "dmarc_addresses" {
  type        = list(string)
  description = "List of all dmarc report email addresses provided in TXT record, addresses should conform to https://docs.aws.amazon.com/ses/latest/DeveloperGuide/receiving-email-receipt-rules.html"
}
