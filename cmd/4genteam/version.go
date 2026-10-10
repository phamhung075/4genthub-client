package main

// clientVersion is the CLIENT's own version, and it is deliberately NOT the server's release identity:
// config.ReleaseVersion marks what the server deploys, while this marks the binary on somebody's
// machine - two different questions, and the team has already spent an evening on what happens when
// one string is asked to answer both.
const clientVersion = "0.1.0"
