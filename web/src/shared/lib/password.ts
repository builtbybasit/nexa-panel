const CHARACTER_CLASSES = ['abcdefghijklmnopqrstuvwxyz', 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', '0123456789', '!@#$%^&*-_=+']

function randomBelow(bound: number): number {
  const buffer = new Uint32Array(1)
  crypto.getRandomValues(buffer)
  return (buffer[0] ?? 0) % bound
}

/** Random characters guaranteed to mix all four classes. */
export function generatePassword(length: number): string {
  const all = CHARACTER_CLASSES.join('')
  const chars = CHARACTER_CLASSES.map((set) => set.charAt(randomBelow(set.length)))
  while (chars.length < length) chars.push(all.charAt(randomBelow(all.length)))
  for (let i = chars.length - 1; i > 0; i--) {
    const j = randomBelow(i + 1)
    ;[chars[i], chars[j]] = [chars[j] ?? '', chars[i] ?? '']
  }
  return chars.join('')
}
