output "submgr_arn" {
  value       = aws_lambda_function.submgr.arn
  description = "Subscription manager lambda Arn"
}

output "interim_sub_arn" {
  value       = aws_lambda_function.interim_sub.arn
  description = "Interim sns sub lambda Arn"
}

output "delivery_retrier_arn" {
  value       = aws_lambda_function.delivery_retrier.arn
  description = "Delivery retrier lambda Arn"
}

output "github_action_iam_role_arn" {
  value       = aws_iam_role.actions_role.arn
  description = "Github actions iam role Arn"
}

output "sesrcvr_user_access" {
  value       = "Create a new access key to for use: https://console.aws.amazon.com/iam/home#/users/${aws_iam_user.sesrcvr_user.name}?section=security_credentials"
  description = "Visit the console to create access key id/secret to use with sesrcvr program."
}
