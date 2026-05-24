---
status: "accepted"
summary: "Start with Google Health API instead of Google Fit, Fitbit Web API, or Health Connect."
read_when:
  - "Revisiting provider choice."
  - "Considering legacy Fitbit, Google Fit, or Health Connect first."
---
# Google Health API as Primary Provider

Use Google Health API as the first Provider because it is Google's replacement path for Fitbit Web API data and covers Fitbit, Pixel Watch, and third-party health data behind Google OAuth. We are deliberately not starting with Google Fit, legacy Fitbit Web API, or Health Connect because those either point backward, fragment the data path, or require Android companion infrastructure before a desktop CLI can be useful.
