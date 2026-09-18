/**
 * Copies text to the clipboard. The async Clipboard API only exists in secure
 * contexts (HTTPS or localhost); consoles reached over plain HTTP fall back to
 * a hidden textarea and document.execCommand('copy').
 *
 * `container` should be an element near the trigger (inside open dialogs) so
 * focus traps do not move the selection away from the temporary textarea.
 */
export async function copyText(text: string, container?: HTMLElement | null): Promise<void> {
  if (typeof navigator !== 'undefined' && navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const host = container ?? document.body;
  const textarea = document.createElement('textarea');
  textarea.value = text;
  textarea.setAttribute('readonly', '');
  textarea.setAttribute('aria-hidden', 'true');
  Object.assign(textarea.style, {
    position: 'fixed',
    top: '0',
    left: '0',
    width: '1px',
    height: '1px',
    opacity: '0',
    pointerEvents: 'none',
  });
  const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  host.appendChild(textarea);
  try {
    textarea.focus({ preventScroll: true });
    textarea.select();
    // execCommand is deprecated but remains the only option outside secure contexts.
    if (!document.execCommand('copy')) {
      throw new Error('copy command was rejected');
    }
  } finally {
    textarea.remove();
    active?.focus({ preventScroll: true });
  }
}
