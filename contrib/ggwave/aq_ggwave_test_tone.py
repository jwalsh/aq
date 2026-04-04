#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = ["ggwave", "sounddevice", "numpy"]
# ///
"""Minimal ggwave tone test — play a short chirp per protocol to identify audibility."""
import sys
import ggwave
import numpy as np
import sounddevice as sd

SAMPLE_RATE = 48000
NAMES = [
    "0:audible", "1:audible-fast", "2:audible-fastest",
    "3:ultrasonic", "4:ultrasonic-fast", "5:ultrasonic-fastest",
    "6:dt", "7:dt-fast", "8:dt-fastest",
]

def play(protocol_id: int, volume: int = 50) -> None:
    wav = ggwave.encode("hi", protocolId=protocol_id, volume=volume)
    samples = np.frombuffer(wav, dtype=np.int16).astype(np.float32) / 32768.0
    fft = np.abs(np.fft.rfft(samples[:SAMPLE_RATE]))
    freqs = np.fft.rfftfreq(SAMPLE_RATE, 1 / SAMPLE_RATE)
    peak = freqs[np.argmax(fft[10:]) + 10]
    print(f"  {NAMES[protocol_id]:22s}  peak {peak:,.0f} Hz  {len(samples)/SAMPLE_RATE:.1f}s")
    sd.play(samples, samplerate=SAMPLE_RATE)
    sd.wait()

if __name__ == "__main__":
    pid = int(sys.argv[1]) if len(sys.argv) > 1 else None
    vol = int(sys.argv[2]) if len(sys.argv) > 2 else 50
    if pid is not None:
        play(pid, vol)
    else:
        print("Playing all 9 protocols (2s gap between each):")
        import time
        for i in range(9):
            play(i, vol)
            time.sleep(2)
