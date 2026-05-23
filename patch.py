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
        .expect("Failed to invoke rustc to get target spec json");''', '''    let base_target = if target.ends_with("-rusticated") {
        target.split("-rusticated").next().unwrap().to_string() + "-unknown-linux-gnu"
    } else { target.clone() };
    let output = std::process::Command::new(&rustc)
        .arg("-Z").arg("unstable-options").arg("--print").arg("target-spec-json").arg("--target").arg(&base_target)
        .output().expect("Failed to invoke rustc to get target spec json");''')
code = code.replace('''    obj.insert("no-default-libraries".to_string(), serde_json::json!(true));''', '''    obj.insert("no-default-libraries".to_string(), serde_json::json!(true));
    if let Some(metadata) = obj.get_mut("metadata") {
        if let Some(meta_obj) = metadata.as_object_mut() {
            meta_obj.insert("std".to_string(), serde_json::json!(false));
        }
    } else {
        obj.insert("metadata".to_string(), serde_json::json!({ "std": false }));
    }''')
with open('build.rs', 'w') as f:
    f.write(code)
