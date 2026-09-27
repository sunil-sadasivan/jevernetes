//! Bounded structural evidence shared by shadow quarantine and reviewed rules.
use crate::{controller::digest, events::Source};
use serde::{
    Deserialize, Deserializer,
    de::{self, MapAccess, SeqAccess, Visitor},
};
use serde_json::Value;
use std::{collections::BTreeMap, fmt};

pub const MAX_BYTES: usize = 2048;
// Reject duplicate keys at every depth (serde_json::Value alone keeps the last).
struct Unique(Value);
impl<'de> Deserialize<'de> for Unique {
    fn deserialize<D: Deserializer<'de>>(d: D) -> Result<Self, D::Error> {
        struct V;
        impl<'de> Visitor<'de> for V {
            type Value = Unique;
            fn expecting(&self, f: &mut fmt::Formatter) -> fmt::Result {
                f.write_str("unambiguous JSON")
            }
            fn visit_bool<E: de::Error>(self, v: bool) -> Result<Unique, E> {
                Ok(Unique(v.into()))
            }
            fn visit_i64<E: de::Error>(self, v: i64) -> Result<Unique, E> {
                Ok(Unique(v.into()))
            }
            fn visit_u64<E: de::Error>(self, v: u64) -> Result<Unique, E> {
                Ok(Unique(v.into()))
            }
            fn visit_f64<E: de::Error>(self, v: f64) -> Result<Unique, E> {
                serde_json::Number::from_f64(v)
                    .map(|n| Unique(Value::Number(n)))
                    .ok_or_else(|| E::custom("number"))
            }
            fn visit_str<E: de::Error>(self, v: &str) -> Result<Unique, E> {
                Ok(Unique(v.into()))
            }
            fn visit_unit<E: de::Error>(self) -> Result<Unique, E> {
                Ok(Unique(Value::Null))
            }
            fn visit_seq<A: SeqAccess<'de>>(self, mut a: A) -> Result<Unique, A::Error> {
                let mut out = Vec::new();
                while let Some(Unique(v)) = a.next_element()? {
                    out.push(v);
                }
                Ok(Unique(Value::Array(out)))
            }
            fn visit_map<A: MapAccess<'de>>(self, mut a: A) -> Result<Unique, A::Error> {
                let mut out = serde_json::Map::new();
                while let Some((k, Unique(v))) = a.next_entry::<String, Unique>()? {
                    if out.insert(k, v).is_some() {
                        return Err(de::Error::custom("duplicate key"));
                    }
                }
                Ok(Unique(Value::Object(out)))
            }
        }
        d.deserialize_any(V)
    }
}
pub(crate) fn parse(raw: &[u8], limit: usize) -> Option<Value> {
    if raw.len() > limit {
        return None;
    }
    serde_json::from_slice::<Unique>(raw).ok().map(|v| v.0)
}
#[derive(Clone, Copy, Debug, PartialEq, Eq, serde::Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Kind {
    Object,
    Array,
    String,
    Number,
    Boolean,
    Null,
}
pub fn kind(v: &Value) -> Kind {
    match v {
        Value::Object(_) => Kind::Object,
        Value::Array(_) => Kind::Array,
        Value::String(_) => Kind::String,
        Value::Number(_) => Kind::Number,
        Value::Bool(_) => Kind::Boolean,
        Value::Null => Kind::Null,
    }
}
pub fn pointer(path: &str) -> bool {
    path.len() <= 128
        && (path.is_empty()
            || (path.starts_with('/')
                && path[1..].split('/').all(|s| {
                    !s.is_empty()
                        && s.bytes()
                            .all(|b| b.is_ascii_alphanumeric() || b"_-".contains(&b))
                })))
}
pub fn shape(v: &Value) -> Option<BTreeMap<String, Kind>> {
    fn walk(v: &Value, p: String, depth: usize, out: &mut BTreeMap<String, Kind>) -> Option<()> {
        if depth > 8 || !pointer(&p) || out.len() >= 128 {
            return None;
        }
        out.insert(p.clone(), kind(v));
        match v {
            Value::Object(m) => {
                for (k, v) in m {
                    if k.is_empty() || k.contains('/') {
                        return None;
                    }
                    walk(v, format!("{p}/{k}"), depth + 1, out)?;
                }
            }
            Value::Array(a) => {
                for (i, v) in a.iter().enumerate() {
                    walk(v, format!("{p}/{i}"), depth + 1, out)?;
                }
            }
            _ => (),
        }
        Some(())
    }
    if !v.is_object() {
        return None;
    }
    let mut out = BTreeMap::new();
    walk(v, String::new(), 0, &mut out)?;
    Some(out)
}
pub(crate) fn family(source: &Source, text: &str) -> Option<String> {
    Some(digest(&(
        source,
        shape(&parse(text.as_bytes(), crate::events::MAX_EVENT)?)?,
    )))
}
