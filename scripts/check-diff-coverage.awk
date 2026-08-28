$1 == "module" {
  module_prefix = $2 "/"
  next
}

$1 == "coverage" {
  reading_coverage = 1
  line = $2
  if (line == "mode: atomic" || line == "mode: count" || line == "mode: set") {
    next
  }
}

!reading_coverage {
  changed[$1 SUBSEP $2] = 1
  next
}

reading_coverage {
  line = $0
  split(line, fields, " ")
  location = fields[1]
  statements = fields[2] + 0
  execution_count = fields[3] + 0

  colon = index(location, ":")
  file = substr(location, 1, colon - 1)
  if (index(file, module_prefix) == 1) {
    file = substr(file, length(module_prefix) + 1)
  }

  range = substr(location, colon + 1)
  split(range, endpoints, ",")
  split(endpoints[1], begin_parts, ".")
  split(endpoints[2], end_parts, ".")

  intersects = 0
  for (line_number = begin_parts[1]; line_number <= end_parts[1]; line_number++) {
    if (changed[file SUBSEP line_number]) {
      intersects = 1
      break
    }
  }
  if (intersects) {
    total += statements
    if (execution_count > 0) {
      covered += statements
    }
  }
}

END {
  percentage = total == 0 ? 100 : covered * 100 / total
  printf "%.1f\t%d\t%d", percentage, covered, total
}
