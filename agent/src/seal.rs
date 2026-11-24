//! Sealed jobs (ARCHITECTURE.md §3.3). The sandbox makes a fresh X25519 key per job.
//! The gateway encrypts the job to it; output is encrypted back to the gateway.
//! Keys: HKDF-SHA256(shared, salt = job_id, info = "infera sealed job v1") -> down (32) | up (32).

use anyhow::{anyhow, Result};
use base64::{engine::general_purpose::STANDARD as B64, Engine};
use chacha20poly1305::{aead::Aead, ChaCha20Poly1305, KeyInit, Nonce};
use hkdf::Hkdf;
use rand::RngCore;
use sha2::Sha256;
use x25519_dalek::{PublicKey, StaticSecret};

pub struct SandboxKey {
    secret: StaticSecret,
    pub public_b64: String,
}

impl SandboxKey {
    pub fn new() -> Self {
        let secret = StaticSecret::random_from_rng(rand::rngs::OsRng);
        let public_b64 = B64.encode(PublicKey::from(&secret).as_bytes());
        Self { secret, public_b64 }
    }

    pub fn open(self, job_id: &str, epk_b64: &str) -> Result<SealedChannel> {
        let epk: [u8; 32] = B64.decode(epk_b64)?.try_into().map_err(|_| anyhow!("bad gateway key"))?;
        let shared = self.secret.diffie_hellman(&PublicKey::from(epk));
        let hk = Hkdf::<Sha256>::new(Some(job_id.as_bytes()), shared.as_bytes());
        let mut keys = [0u8; 64];
        hk.expand(b"infera sealed job v1", &mut keys).map_err(|_| anyhow!("hkdf"))?;
        Ok(SealedChannel {
            down: ChaCha20Poly1305::new_from_slice(&keys[..32]).unwrap(),
            up: ChaCha20Poly1305::new_from_slice(&keys[32..]).unwrap(),
        })
    }
}

pub struct SealedChannel {
    down: ChaCha20Poly1305,
    up: ChaCha20Poly1305,
}

impl SealedChannel {
    pub fn open_down(&self, nonce_b64: &str, ct_b64: &str) -> Result<Vec<u8>> {
        let n = B64.decode(nonce_b64)?;
        let ct = B64.decode(ct_b64)?;
        if n.len() != 12 {
            return Err(anyhow!("bad nonce"));
        }
        self.down.decrypt(Nonce::from_slice(&n), ct.as_ref()).map_err(|_| anyhow!("job failed authentication"))
    }

    pub fn seal_up(&self, plain: &[u8]) -> (String, String) {
        let mut n = [0u8; 12];
        rand::rngs::OsRng.fill_bytes(&mut n);
        let ct = self.up.encrypt(Nonce::from_slice(&n), plain).expect("encrypt");
        (B64.encode(n), B64.encode(ct))
    }
}
