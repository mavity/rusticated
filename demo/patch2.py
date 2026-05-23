import re

with open('build.rs', 'r') as f:
    code = f.read()

code = code.replace('''    let output = std::process::Command::new(&rustc)
        .arg("-Z")
        .arg("unstable-options")
        .arg("--print")
        .arg("target-spec-json")
        .arg("--target")
        .arg(&target)
        .output()
        .expect("Failed to invoke rustc to get target spec json");''', '''    
    // If the target is already our custom target, base it on the base architect
ure
    let base_target = if target.ends_with("-rusticated") {
        target.split("-rusticated").next().unwrap().to_string() + "-unknown-linu
x-gnu"
    } else {
        target.clone()
    };
        
    let output = std::process::Command::new(&rustc)
        .arg("-Z")
        .arg("unstable-options")
        .arg("--print")
        .arg("target-spec-json")
        .arg("--target")
        .arg(&base_target)
        .output()
        .expect("Failed to invoke rustc to get target spec json");''')

with open('build.rs', 'w') as f:
    f.write(code)
