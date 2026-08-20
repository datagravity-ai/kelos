const form = document.querySelector('#login-form');
const input = document.querySelector('#token');
const error = document.querySelector('#login-error');
const reveal = document.querySelector('#reveal');

reveal.addEventListener('click', () => {
  const hidden = input.type === 'password';
  input.type = hidden ? 'text' : 'password';
  reveal.textContent = hidden ? 'Hide' : 'Show';
});

form.addEventListener('submit', async (event) => {
  event.preventDefault();
  error.textContent = '';
  const button = form.querySelector('button[type="submit"]');
  button.disabled = true;
  try {
    const response = await fetch('/api/login', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({token: input.value}),
    });
    if (!response.ok) {
      const body = await response.json().catch(() => ({}));
      throw new Error(body.error || 'Sign-in failed');
    }
    window.location.replace('/');
  } catch (cause) {
    error.textContent = cause.message;
    input.focus();
    input.select();
  } finally {
    button.disabled = false;
  }
});
