| Path | Build | RACK | Runs | Over 1 s (20 MB: 5 s) | Crawls | 30 s timeouts | Median s | Max s | Total RTOs | Median queue drops |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 3 ms, 80 Mbit/s, queue 50 | pin (v1.104.0–v1.104.4) | on | 12 | 10 | 10 | 4 | 19.5 | 30.0 | 1029 | 1000.5 |
| 3 ms, 80 Mbit/s, queue 50 | pin (v1.104.5, main) | off | 12 | 1 | 0 | 0 | 0.87 | 1.03 | 33 | 753.5 |
| 3 ms, 80 Mbit/s, queue 50 | master | on | 12 | 0 | 0 | 0 | 0.30 | 0.33 | 0 | 0.5 |
| 3 ms, 80 Mbit/s, queue 50 | master | off | 12 | 0 | 0 | 0 | 0.30 | 0.50 | 1 | 1 |
| 3 ms, 80 Mbit/s, queue 100 | pin (v1.104.0–v1.104.4) | on | 12 | 7 | 6 | 0 | 8.24 | 29.6 | 677 | 584 |
| 3 ms, 80 Mbit/s, queue 100 | pin (v1.104.5, main) | off | 12 | 0 | 0 | 0 | 0.70 | 0.90 | 20 | 644 |
| 3 ms, 80 Mbit/s, queue 100 | master | on | 12 | 0 | 0 | 0 | 0.30 | 0.56 | 0 | 0 |
| 3 ms, 80 Mbit/s, queue 100 | master | off | 12 | 1 | 0 | 0 | 0.30 | 1.02 | 1 | 0 |
| 3 ms, 80 Mbit/s, queue 256 | pin (v1.104.0–v1.104.4) | on | 12 | 1 | 1 | 0 | 0.31 | 12.6 | 59 | 154.5 |
| 3 ms, 80 Mbit/s, queue 256 | pin (v1.104.5, main) | off | 12 | 1 | 0 | 0 | 0.51 | 1.61 | 6 | 174 |
| 3 ms, 80 Mbit/s, queue 256 | master | on | 12 | 1 | 0 | 0 | 0.34 | 1.29 | 0 | 0 |
| 3 ms, 80 Mbit/s, queue 256 | master | off | 12 | 1 | 0 | 0 | 0.38 | 1.05 | 0 | 0 |
| 20 ms, 100 Mbit/s, queue 100 | pin (v1.104.0–v1.104.4) | on | 8 | 1 | 0 | 0 | 0.38 | 1.98 | 1 | 20.5 |
| 20 ms, 100 Mbit/s, queue 100 | pin (v1.104.5, main) | off | 8 | 0 | 0 | 0 | 0.52 | 0.91 | 2 | 33.5 |
| 20 ms, 100 Mbit/s, queue 100 | master | on | 8 | 1 | 0 | 0 | 0.43 | 1.82 | 1 | 10.5 |
| 20 ms, 100 Mbit/s, queue 100 | master | off | 8 | 0 | 0 | 0 | 0.39 | 0.92 | 2 | 127.5 |
| 3 ms, 80 Mbit/s, queue 100, client timestamps off | pin (v1.104.0–v1.104.4) | on | 8 | 0 | 0 | 0 | 0.31 | 0.35 | 0 | 781 |
| 3 ms, 80 Mbit/s, queue 100, 20 MB fetch | pin (v1.104.0–v1.104.4) | on | 8 | 3 | 3 | 1 | 2.30 | 30.0 | 337 | 789.5 |
| 3 ms, 80 Mbit/s, queue 100, 20 MB fetch | pin (v1.104.5, main) | off | 8 | 0 | 0 | 0 | 2.52 | 2.90 | 16 | 835 |
| 3 ms, 80 Mbit/s, queue 100, 20 MB fetch | master | on | 8 | 0 | 0 | 0 | 2.11 | 2.30 | 0 | 2 |
| 3 ms, 80 Mbit/s, queue 100, 20 MB fetch | master | off | 8 | 0 | 0 | 0 | 2.10 | 2.11 | 0 | 3 |
| 3 ms, 80 Mbit/s, queue 100 (2nd pass, seeds 13-16) | pin (v1.104.0–v1.104.4) | on | 4 | 4 | 4 | 0 | 7.29 | 15.3 | 154 | 743 |
| 3 ms, 80 Mbit/s, queue 100 (2nd pass, seeds 13-16) | pin (v1.104.5, main) | off | 4 | 0 | 0 | 0 | 0.69 | 0.92 | 9 | 758.5 |
| 3 ms, 80 Mbit/s, queue 100 (2nd pass, seeds 13-16) | master | on | 4 | 0 | 0 | 0 | 0.30 | 0.30 | 0 | 0 |
| 3 ms, 80 Mbit/s, queue 100 (2nd pass, seeds 13-16) | master | off | 4 | 0 | 0 | 0 | 0.30 | 0.31 | 0 | 0 |
| 3 ms, 80 Mbit/s, queue 256 (2nd pass, seeds 13-16) | pin (v1.104.0–v1.104.4) | on | 4 | 1 | 1 | 0 | 0.31 | 13.9 | 65 | 305.5 |
| 3 ms, 80 Mbit/s, queue 256 (2nd pass, seeds 13-16) | pin (v1.104.5, main) | off | 4 | 0 | 0 | 0 | 0.51 | 0.54 | 3 | 156.5 |
| 3 ms, 80 Mbit/s, queue 256 (2nd pass, seeds 13-16) | master | on | 4 | 0 | 0 | 0 | 0.30 | 0.30 | 0 | 0 |
| 3 ms, 80 Mbit/s, queue 256 (2nd pass, seeds 13-16) | master | off | 4 | 0 | 0 | 0 | 0.30 | 0.30 | 0 | 0 |
| 20 ms, 100 Mbit/s, queue 100 (2nd pass, seeds 9-16) | pin (v1.104.0–v1.104.4) | on | 8 | 0 | 0 | 0 | 0.35 | 0.58 | 0 | 6.5 |
| 20 ms, 100 Mbit/s, queue 100 (2nd pass, seeds 9-16) | pin (v1.104.5, main) | off | 8 | 0 | 0 | 0 | 0.35 | 0.80 | 1 | 6.5 |
| 20 ms, 100 Mbit/s, queue 100 (2nd pass, seeds 9-16) | master | on | 8 | 0 | 0 | 0 | 0.35 | 0.57 | 0 | 8 |
| 20 ms, 100 Mbit/s, queue 100 (2nd pass, seeds 9-16) | master | off | 8 | 0 | 0 | 0 | 0.35 | 0.35 | 0 | 6.5 |
