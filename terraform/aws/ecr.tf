resource "aws_ecr_repository" "bench" {
  name                 = "${var.project}/bench"
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }
}

resource "aws_ecr_repository" "probe" {
  name                 = "${var.project}/probe"
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }
}

# Untagged images are replaced on every push and otherwise bill forever.
resource "aws_ecr_lifecycle_policy" "expire_untagged" {
  for_each   = { bench = aws_ecr_repository.bench.name, probe = aws_ecr_repository.probe.name }
  repository = each.value

  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Expire untagged images after 7 days"
      selection    = { tagStatus = "untagged", countType = "sinceImagePushed", countUnit = "days", countNumber = 7 }
      action       = { type = "expire" }
    }]
  })
}
