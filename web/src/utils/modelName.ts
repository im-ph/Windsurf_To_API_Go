// Pretty-print a Windsurf catalog key. Mirrors go/internal/models.DisplayName
// so the dashboard shows "Claude-Opus-4.6-Thinking" rather than the raw
// lowercase id in every table. Kept as a pure function (no catalog fetch)
// because the transformation is deterministic from the key alone.

const ACRONYMS = new Set(['gpt', 'glm', 'swe', 'oss']);

function isVersionish(s: string): boolean {
  if (!s) return false;
  let hasDigit = false;
  for (const c of s) {
    if (c >= '0' && c <= '9') {
      hasDigit = true;
      continue;
    }
    if (c === '.' || c === 'b' || c === 'B' || c === 'm' || c === 'M' || c === 'k' || c === 'K') continue;
    return false;
  }
  return hasDigit;
}

function isPureDigits(s: string): boolean {
  if (!s) return false;
  for (const c of s) {
    if (c < '0' || c > '9') return false;
  }
  return true;
}

export function displayModelName(id: string): string {
  if (!id) return '';
  const parts = id.split('-');
  const out: string[] = [];
  for (let i = 0; i < parts.length; i++) {
    const seg = parts[i];
    if (!seg) continue;
    if (ACRONYMS.has(seg.toLowerCase())) {
      out.push(seg.toUpperCase());
      continue;
    }
    // Adjacent pure-digit tokens (cloud modelUIDs like "claude-opus-4-7-high")
    // merge back into dotted versions so "4-7" → "4.7".
    if (isPureDigits(seg) && i + 1 < parts.length && isPureDigits(parts[i + 1])) {
      out.push(`${seg}.${parts[i + 1]}`);
      i++;
      continue;
    }
    if (isVersionish(seg)) {
      out.push(seg);
      continue;
    }
    out.push(seg[0].toUpperCase() + seg.slice(1));
  }
  return out.join(' ');
}
