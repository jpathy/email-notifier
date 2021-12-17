terraform {
  required_version = "~> 1.1"
  cloud {
    workspaces {
      name = "email-notifier"
    }
  }
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 3.0"
    }
  }
}

provider "aws" {
  region = var.aws_region
  default_tags {
    tags = {
      usage = var.stack
    }
  }
}

data "aws_caller_identity" "current" {}

module "gen" {
  source = "./gen_consts"
}

locals {
  idx_name = "${var.stack}-ts-sorted-index"
}

resource "aws_dynamodb_table" "notifier_db" {
  name           = "${var.stack}-db"
  read_capacity  = 5
  write_capacity = 5
  hash_key       = "Arn"
  range_key      = "UniqId"

  attribute {
    name = module.gen.dynamodb_table_hashkey_attrname
    type = "S"
  }

  attribute {
    name = module.gen.dynamodb_table_rangekey_attrname
    type = "S"
  }

  attribute {
    name = module.gen.dynamodb_idx_rangekey_attrname
    type = "S"
  }

  local_secondary_index {
    name            = local.idx_name
    range_key       = module.gen.dynamodb_idx_rangekey_attrname
    projection_type = "KEYS_ONLY"
  }
}
