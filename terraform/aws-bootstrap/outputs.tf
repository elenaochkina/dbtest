# Each user puts this in ~/.aws/config as role_arn under a profile.
output "role_arn" {
  value = aws_iam_role.dbtest.arn
}
