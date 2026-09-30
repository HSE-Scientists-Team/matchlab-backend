#!/usr/bin/env bash
set -euo pipefail

mkdir -p coverage
profile=coverage/coverage.out

# Все тесты репозитория запускаются один раз. Покрытие измеряется только для internal.
go test ./... -coverpkg=./internal/... -coverprofile="$profile"

module=$(go list -m)
summary="${GITHUB_STEP_SUMMARY:-/dev/null}"
printf '\nПокрытие по сервисам (только internal):\n'
printf '\n## Покрытие по сервисам\n\n| Сервис | Покрытие |\n| --- | ---: |\n' >> "$summary"

while IFS= read -r service; do
    coverage=$(awk -v prefix="$module/internal/$service/" '
        NR > 1 && index($1, prefix) == 1 {
            blocks[$1] = $2
            if ($3 > 0) hit[$1] = 1
        }
        END {
            for (block in blocks) {
                total += blocks[block]
                if (hit[block]) covered += blocks[block]
            }
            if (total == 0) print "н/д (нет кода для измерения)"
            else printf "%.1f%% (%d/%d инструкций)", 100 * covered / total, covered, total
        }
    ' "$profile")
    printf '%s: %s\n' "$service" "$coverage"
    printf '| %s | %s |\n' "$service" "$coverage" >> "$summary"
done < <(go list ./internal/... | awk -v prefix="$module/internal/" '
    index($0, prefix) == 1 {
        name = substr($0, length(prefix) + 1)
        split(name, parts, "/")
        print parts[1]
    }
' | sort -u)
