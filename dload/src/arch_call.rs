pub unsafe fn call_c_func(sym: usize, args: &[u64]) -> u64 {
    unsafe {
        match args.len() {
            0 => {
                let f: unsafe extern "C" fn() -> usize = core::mem::transmute(sym);
                f() as u64
            }
            1 => {
                let f: unsafe extern "C" fn(usize) -> usize = core::mem::transmute(sym);
                f(args[0] as usize) as u64
            }
            2 => {
                let f: unsafe extern "C" fn(usize, usize) -> usize = core::mem::transmute(sym);
                f(args[0] as usize, args[1] as usize) as u64
            }
            3 => {
                let f: unsafe extern "C" fn(usize, usize, usize) -> usize = core::mem::transmute(sym);
                f(args[0] as usize, args[1] as usize, args[2] as usize) as u64
            }
            4 => {
                let f: unsafe extern "C" fn(usize, usize, usize, usize) -> usize = core::mem::transmute(sym);
                f(args[0] as usize, args[1] as usize, args[2] as usize, args[3] as usize) as u64
            }
            5 => {
                let f: unsafe extern "C" fn(usize, usize, usize, usize, usize) -> usize = core::mem::transmute(sym);
                f(args[0] as usize, args[1] as usize, args[2] as usize, args[3] as usize, args[4] as usize) as u64
            }
            6 => {
                let f: unsafe extern "C" fn(usize, usize, usize, usize, usize, usize) -> usize = core::mem::transmute(sym);
                f(args[0] as usize, args[1] as usize, args[2] as usize, args[3] as usize, args[4] as usize, args[5] as usize) as u64
            }
            7 => {
                let f: unsafe extern "C" fn(usize, usize, usize, usize, usize, usize, usize) -> usize = core::mem::transmute(sym);
                f(args[0] as usize, args[1] as usize, args[2] as usize, args[3] as usize, args[4] as usize, args[5] as usize, args[6] as usize) as u64
            }
            _ => {
                let f: unsafe extern "C" fn(usize, usize, usize, usize, usize, usize, usize, usize) -> usize = core::mem::transmute(sym);
                f(args[0] as usize, args[1] as usize, args[2] as usize, args[3] as usize, args[4] as usize, args[5] as usize, args[6] as usize, args[7] as usize) as u64
            }
        }
    }
}
