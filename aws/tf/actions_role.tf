resource "aws_iam_openid_connect_provider" "github_oidc" {
  url             = "https://token.actions.githubusercontent.com"
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = ["6938fd4d98bab03faadb97b34396831e3780aea1"]
}

data "aws_iam_policy_document" "actions_policy_data" {
  statement {
    sid       = "lambdaDeploy"
    actions   = ["lambda:UpdateFunctionCode"]
    resources = ["*"]
  }
}

resource "aws_iam_policy" "actions_policy" {
  name_prefix = "${var.github_repo}-GithubActions-"
  path        = "/GithubActions/${var.github_org}/"
  description = "role policy attached to role assumed by action runners in repo:${var.github_org}/${var.github_repo}"
  policy      = data.aws_iam_policy_document.actions_policy_data.json
}

data "aws_iam_policy_document" "actions_assume_role_policy_data" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github_oidc.arn]
    }
    condition {
      test     = "StringLike"
      values   = ["repo:${var.github_org}/${var.github_repo}:*"]
      variable = "token.actions.githubusercontent.com:sub"
    }
  }
}

resource "aws_iam_role" "actions_role" {
  name_prefix        = "${var.github_repo}-GithubActions-"
  path               = "/GithubActions/${var.github_org}/"
  description        = "role for all actions used by repo:${var.github_org}/${var.github_repo}"
  assume_role_policy = data.aws_iam_policy_document.actions_assume_role_policy_data.json
}

resource "aws_iam_role_policy_attachment" "actions_role_attach" {
  role       = aws_iam_role.actions_role.name
  policy_arn = aws_iam_policy.actions_policy.arn
}
