module "first" {
  source      = "./child"
  parallelism = 1
}

module "second" {
  source      = "./child"
  parallelism = 1
}

output "first" {
  value = module.first.status
}

output "second" {
  value = module.second.status
}
