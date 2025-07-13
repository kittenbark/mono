## `mono` — making monoliths comfy (currently in an early development stage)

Minimal HTML/JS modern websites with Tailwind support out of the box.

Why this library exists — I love the idea behind Next.js almost as much as I hate JavaScript.
And for the love of God, 'compiling' html+js shouldn't take minutes.

### Features

- Zero dependencies — only standard Go library (embrace `template.HTML`)
- Fine grain control over requests, easily add dynamic handlers/apis
- As lightweight as it gets
- Dev builds take milliseconds (~5ms in my case)
- Prod builds take seconds (~5s, 95% of time takes Tailwind CLI)

All it takes is <2k lines of code.
