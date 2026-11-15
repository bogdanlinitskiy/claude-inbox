'use strict';

// beep is two short notes from an oscillator rather than an audio file: nothing to vendor, nothing to fetch,
// and it works with the page offline like everything else here. It plays only once unlockAudio has run: a
// suspended context would hold every note and play them all at once on the next click.
function beep() {
  if (!soundOn || audio?.state !== 'running') return;
  try {
    const at = audio.currentTime;
    const gain = audio.createGain();
    gain.connect(audio.destination);
    // Ramps, not steps: a square edge on the gain is heard as a click.
    gain.gain.setValueAtTime(0.0001, at);
    gain.gain.exponentialRampToValueAtTime(0.12, at + 0.01);
    gain.gain.exponentialRampToValueAtTime(0.0001, at + 0.34);
    const osc = audio.createOscillator();
    osc.type = 'sine';
    osc.frequency.setValueAtTime(660, at);
    osc.frequency.setValueAtTime(880, at + 0.12);
    osc.connect(gain);
    osc.start(at);
    osc.stop(at + 0.35);
  } catch { /* no audio device, or the browser refused: the toast and the notification still fire */ }
}

// unlockAudio opens or resumes the audio context. Browsers allow that only inside a user gesture, so it runs
// from clicks and keys, never from an alert.
function unlockAudio() {
  if (!soundOn) return Promise.resolve();
  try {
    audio ||= new (window.AudioContext || window.webkitAudioContext)();
    return audio.state === 'running' ? Promise.resolve() : audio.resume();
  } catch (err) {
    return Promise.reject(err);
  }
}

// Sound left on from the last visit has no click behind it after a reload: the first one on the page unlocks it.
for (const type of ['pointerdown', 'keydown']) {
  document.addEventListener(type, () => { unlockAudio().catch(() => {}); }, { capture: true });
}

function renderSound() {
  const b = $('#sound');
  b.setAttribute('aria-pressed', String(soundOn));
  b.textContent = 'Sound';
  b.title = soundOn ? 'Sound is on: a short sound when a session starts waiting' : 'Sound is off';
}

$('#sound').addEventListener('click', () => {
  soundOn = !soundOn;
  writeSetting('sound', soundOn);
  renderSound();
  // Play it once on the way on: it confirms the setting, and this click is the gesture that unlocks audio.
  if (soundOn) unlockAudio().then(beep).catch(() => {});
});

function renderAlerts() {
  const b = $('#alerts');
  b.setAttribute('aria-pressed', String(alertsOn));
  b.textContent = 'Alerts';
  b.title = alertsOn ? 'Alerts are on: a desktop notification when a session starts waiting' : 'Alerts are off';
}

$('#alerts').addEventListener('click', async () => {
  alertsOn = !alertsOn;
  writeSetting('alerts', alertsOn);
  renderAlerts();
  if (!alertsOn || !('Notification' in window)) return;
  if (Notification.permission === 'default') await Notification.requestPermission();
  if (Notification.permission === 'denied') {
    toast('Desktop notifications are blocked', 'Allow them in Chrome\'s site settings for this page. Alerts inside the page still work.');
  }
});
