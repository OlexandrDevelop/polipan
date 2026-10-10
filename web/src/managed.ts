// SPDX-License-Identifier: Apache-2.0

// The server lists the fields that polipan.yaml sets as paths, for example config.image or config.extra.replicas
// (services.git_managed_fields). The screens ask by the name of a form field.
const serverPaths: Record<string, string[]> = {
  source: ['config.git'],
  image: ['config.image'],
  ports: ['config.port', 'config.host_port', 'config.extra.ports'],
  command: ['config.command', 'config.entrypoint'],
  replicas: ['config.extra.replicas'],
  resources: ['config.resources'],
  strategy: ['config.strategy', 'config.strategy_options'],
  pre_deploy: ['config.pre_deploy'],
  post_deploy: ['config.post_deploy'],
  health: ['config.health'],
  domains: ['domains'],
  volumes: ['volumes'],
  failover: ['failover'],
}

// isManaged tells if polipan.yaml sets a form field. A path matches itself and the paths below it.
export function isManaged(fields: readonly string[] | undefined, field: string): boolean {
  const list = fields ?? []
  if (list.includes(field)) return true
  const wanted = serverPaths[field] ?? []
  return list.some((f) => wanted.some((w) => f === w || f.startsWith(w + '.')))
}
