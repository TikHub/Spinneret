/** Reports whether a field error map contains at least one error. */
export function hasErrors(errors: object): boolean {
  return Object.values(errors).some((value) => value !== undefined);
}

/**
 * Turns a permission or scope name into a translation key segment: i18next
 * treats ":" as the namespace separator ("config:publish" -> "config_publish").
 */
export function toKeySegment(name: string): string {
  return name.replace(/:/g, '_');
}
