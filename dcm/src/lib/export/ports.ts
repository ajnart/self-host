import type { PortChange } from "./types"

const TOP_LEVEL_KEY = /^([A-Za-z0-9_-]+):\s*$/
const SERVICE_KEY = /^ {2}([^\s#][^:]*):\s*(?:#.*)?$/
const PORT_MAPPING =
  /^( {6}- ["']?(?:(?:\d{1,3}\.){3}\d{1,3}:|\[[^\]]+\]:)?)(\d+)(:\d+(?:\/\w+)?["']?(?:\s.*)?\s*)$/
const SERVICE_PROPERTY = /^ {4}([A-Za-z0-9_-]+):/

function rewriteEnvironmentPort(
  content: string,
  service: string,
  from: string,
  to: number,
): string {
  const hostReference = new RegExp(
    `(\\$\\{LAN_IP(?::?-[^}]*)?\\}):${from}(?!\\d)`,
    "g",
  )
  let inServices = false
  let inService = false
  let inEnvironment = false

  return content
    .split("\n")
    .map((line) => {
      const top = line.match(TOP_LEVEL_KEY)
      if (top) {
        inServices = top[1] === "services"
        inService = false
        inEnvironment = false
        return line
      }
      if (!inServices) return line

      const header = line.match(SERVICE_KEY)
      if (header) {
        inService = header[1] === service
        inEnvironment = false
        return line
      }
      if (!inService) return line

      const property = line.match(SERVICE_PROPERTY)
      if (property) {
        inEnvironment = property[1] === "environment"
        return line
      }
      if (!inEnvironment || line.trim().startsWith("#")) return line

      return line.replace(hostReference, `$1:${to}`)
    })
    .join("\n")
}

export function fixPortConflicts(content: string): {
  content: string
  changes: PortChange[]
} {
  const lines = content.split("\n")
  const mappings = new Map<number, Map<string, number[]>>()
  let inServices = false
  let service: string | undefined
  let inPorts = false

  for (const [index, line] of lines.entries()) {
    const top = line.match(TOP_LEVEL_KEY)
    if (top) {
      inServices = top[1] === "services"
      service = undefined
      inPorts = false
      continue
    }
    if (!inServices) continue

    const header = line.match(SERVICE_KEY)
    if (header) {
      service = header[1]
      inPorts = false
      continue
    }

    const property = line.match(SERVICE_PROPERTY)
    if (property) inPorts = property[1] === "ports"
    if (!service || !inPorts) continue

    const mapping = line.match(PORT_MAPPING)
    if (!mapping) continue
    const port = Number(mapping[2])
    const services = mappings.get(port) ?? new Map<string, number[]>()
    const indexes = services.get(service) ?? []
    indexes.push(index)
    services.set(service, indexes)
    mappings.set(port, services)
  }

  const changes: PortChange[] = []
  const allocatedPorts = new Set(mappings.keys())
  const ports = [...mappings].sort(([a], [b]) => a - b)
  for (const [port, services] of ports) {
    const [[keptBy], ...conflicts] = services
    for (const [service, indexes] of conflicts) {
      let newPort = port + 1
      while (allocatedPorts.has(newPort)) newPort++
      allocatedPorts.add(newPort)
      changes.push({ service, keptBy, from: port, to: newPort })
      for (const index of indexes) {
        lines[index] = lines[index].replace(
          PORT_MAPPING,
          (_, prefix, _port, suffix) => `${prefix}${newPort}${suffix}`,
        )
      }
    }
  }

  let result = lines.join("\n")
  for (const { service, from, to } of changes) {
    result = rewriteEnvironmentPort(result, service, String(from), to)
  }
  return { content: result, changes }
}
