package main

// Pure-Go SQLite driver (no C compiler needed, works on Windows).
import _ "modernc.org/sqlite"

const sqlDriver = "sqlite"

// SQLite connection options (foreign keys on, wait if the file is busy).
const pragmas = "?_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)"
