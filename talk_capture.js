// The phone's microphone, on the audio thread: whatever rate the phone records at (usually
// 48 kHz, sometimes 44.1) down to 24 kHz 16-bit mono, 0.15s at a time. Plain PCM on
// purpose: nothing to decode at the other end, and it's a test tone in, the same tone out.
// A chunk is 45 KB/s, about 0.4 Mbit/s: small beside the stream the same wifi is carrying.
var TALK_RATE = 24000, TALK_CHUNK = 3600;

// Resampler: each output sample is the average of the input it covers, so going down in
// rate doesn't fold high frequencies (wind, mostly) back in as noise. A fractional step
// carries across calls, so a rate that doesn't divide evenly doesn't drift.
function TalkResampler(fromRate, toRate) {
  this.step = fromRate / toRate;
  this.need = this.step;
  this.sum = 0;
  this.n = 0;
}
TalkResampler.prototype.push = function (input, out) {
  for (var i = 0; i < input.length; i++) {
    var take = 1;
    while (take >= this.need) {
      this.sum += input[i] * this.need;
      this.n += this.need;
      take -= this.need;
      out.push(this.sum / this.n);
      this.sum = 0; this.n = 0; this.need = this.step;
    }
    this.sum += input[i] * take;
    this.n += take;
    this.need -= take;
  }
};

function talkToPCM(samples) {
  var pcm = new Int16Array(samples.length);
  for (var i = 0; i < samples.length; i++) {
    var s = Math.max(-1, Math.min(1, samples[i]));
    pcm[i] = s < 0 ? s * 32768 : s * 32767;
  }
  return pcm;
}

if (typeof AudioWorkletProcessor !== 'undefined') {
  registerProcessor('talk-capture', class extends AudioWorkletProcessor {
    constructor() {
      super();
      this.r = new TalkResampler(sampleRate, TALK_RATE);
      this.buf = [];
    }
    process(inputs) {
      var ch = inputs[0] && inputs[0][0];
      if (ch) {
        this.r.push(ch, this.buf);
        while (this.buf.length >= TALK_CHUNK) {
          var part = this.buf.splice(0, TALK_CHUNK), pcm = talkToPCM(part), peak = 0;
          for (var i = 0; i < part.length; i++) peak = Math.max(peak, Math.abs(part[i]));
          this.port.postMessage({pcm: pcm.buffer, peak: peak}, [pcm.buffer]);
        }
      }
      return true;
    }
  });
}
if (typeof module !== 'undefined') module.exports = {TalkResampler: TalkResampler, talkToPCM: talkToPCM, TALK_RATE: TALK_RATE, TALK_CHUNK: TALK_CHUNK};
