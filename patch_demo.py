import re
with open('demo/src/main.rs', 'r') as f:
    code = f.read()

code = code.replace('''        Err(e) => {
            let msg = format!("Could not stat executable: {}\n", e);
            write_all(&mut out, msg.as_bytes()).await;
        }''', '''        Err(e) => {
            let msg = format!("Could not stat executable '{}': code={:?} err={}\n", exe, e.raw_os_error(), e);
            write_all(&mut out, msg.as_bytes()).await;
        }''')

with open('demo/src/main.rs', 'w') as f:
    f.write(code)
