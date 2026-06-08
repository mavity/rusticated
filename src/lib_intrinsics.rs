#[unsafe(no_mangle)]
pub unsafe extern "C" fn fmod(x: f64, y: f64) -> f64 {
    if y == 0.0 {
        return 0.0;
    }
    let div = x / y;
    let int_part = div as i64;
    x - (int_part as f64) * y
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn fmodf(x: f32, y: f32) -> f32 {
    if y == 0.0 {
        return 0.0;
    }
    let div = x / y;
    let int_part = div as i32;
    x - (int_part as f32) * y
}
