output "url" {
  value = github_repository.sandbox.html_url
}

output "issues" {
  description = "Fixture id to issue number."
  value       = { for id, i in github_issue.fixture : id => i.number }
}

output "pull_requests" {
  description = "Fixture id to pull request number."
  value       = { for id, d in data.github_repository_pull_requests.fixture : id => try(d.results[0].number, null) }
}

output "factory" {
  description = "The configuration repository."
  value       = github_repository.factory.full_name
}
